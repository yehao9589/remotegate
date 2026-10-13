package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"
)

const remoteUploadLimit = int64(4 << 30)
const remoteUploadChunk = 256 << 10

type remoteUpload struct {
	file          *sftp.File
	temp, target  string
	size, written int64
	replace       bool
	mode          os.FileMode
	owner         *sftp.FileStat
	lastOffset    int64
	lastHash      [32]byte
	lastSize      int
}
type remoteFileInput struct {
	Action, Path, Name, UploadID string
	Size, Offset                 int64
	Replace                      bool
	Data                         []byte
}
type remoteFileEntry struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Directory  bool   `json:"directory"`
	Symlink    bool   `json:"symlink"`
	ModifiedAt string `json:"modifiedAt"`
	Mode       string `json:"mode"`
}

func remotePath(value, home string) (string, error) {
	if value == "" {
		value = home
	}
	if len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("文件路径无效")
	}
	if !path.IsAbs(value) {
		value = path.Join(home, value)
	}
	return path.Clean(value), nil
}
func remoteName(value string) bool {
	return value != "" && value != "." && value != ".." && len(value) <= 255 && utf8.ValidString(value) && !strings.ContainsAny(value, "/\\\x00\r\n")
}

func (s *remoteSession) initFiles() error {
	if s.files != nil {
		return nil
	}
	client, err := sftp.NewClient(s.ssh, sftp.UseConcurrentReads(false))
	if err != nil {
		return errors.New("SSH 服务未提供 SFTP。OpenWrt 可安装 openssh-sftp-server；安装前请确认路由器软件源可用")
	}
	home, err := client.Getwd()
	if err != nil {
		client.Close()
		return errors.New("无法读取 SSH 用户的初始目录")
	}
	s.files, s.home = client, home
	return nil
}
func (s *remoteSession) fileAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || !sameAuthOrigin(r) {
		http.Error(w, "文件操作请求方式或来源不匹配", 403)
		return
	}
	var input remoteFileInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "文件操作参数无效", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	stop := context.AfterFunc(ctx, func() { _ = s.conn.Close() })
	defer func() { stop(); cancel() }()
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if err := s.initFiles(); err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	target, err := remotePath(input.Path, s.home)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var result any = map[string]bool{"ok": true}
	switch input.Action {
	case "list":
		var entries []os.FileInfo
		entries, err = s.files.ReadDirContext(ctx, target)
		if err == nil {
			sort.Slice(entries, func(i, j int) bool {
				if entries[i].IsDir() != entries[j].IsDir() {
					return entries[i].IsDir()
				}
				return entries[i].Name() < entries[j].Name()
			})
			truncated := len(entries) > 2000
			if truncated {
				entries = entries[:2000]
			}
			files := make([]remoteFileEntry, 0, len(entries))
			for _, entry := range entries {
				files = append(files, remoteFileEntry{entry.Name(), entry.Size(), entry.IsDir(), entry.Mode()&os.ModeSymlink != 0, entry.ModTime().UTC().Format(time.RFC3339), entry.Mode().String()})
			}
			result = map[string]any{"path": target, "home": s.home, "entries": files, "truncated": truncated, "chunkBytes": remoteUploadChunk, "uploadLimit": remoteUploadLimit}
		}
	case "mkdir":
		if !remoteName(input.Name) {
			http.Error(w, "文件夹名称无效", 400)
			return
		}
		err = s.files.Mkdir(path.Join(target, input.Name))
	case "rename":
		if target == "/" || !remoteName(input.Name) {
			http.Error(w, "文件名称或路径无效", 400)
			return
		}
		destination := path.Join(path.Dir(target), input.Name)
		if _, statErr := s.files.Lstat(destination); !os.IsNotExist(statErr) {
			http.Error(w, "目标名称已存在或无法读取，请换一个名称", 409)
			return
		}
		err = s.files.Rename(target, destination)
	case "delete":
		if target == "/" {
			http.Error(w, "不能删除根目录", 400)
			return
		}
		var info os.FileInfo
		info, err = s.files.Lstat(target)
		if err == nil {
			if info.IsDir() {
				err = s.files.RemoveDirectory(target)
			} else {
				err = s.files.Remove(target)
			}
		}
	case "download":
		var info os.FileInfo
		info, err = s.files.Stat(target)
		if err == nil && !info.Mode().IsRegular() {
			err = errors.New("仅支持下载普通文件")
		}
		if err == nil {
			if len(s.downloads) >= 8 {
				http.Error(w, "下载准备请求过多，请重新连接后重试", 429)
				return
			}
			ticket := makeID() + makeID()
			s.downloads[ticket] = target
			result = map[string]string{"url": "/api/admin/remote/" + s.id + "/download?ticket=" + ticket, "name": path.Base(target)}
		}
	case "upload-start":
		if !remoteName(input.Name) || input.Size < 0 || input.Size > remoteUploadLimit || len(s.uploads) >= 1 {
			http.Error(w, "文件名称或大小无效，或已有上传进行中（单文件最多 4 GB）", 400)
			return
		}
		destination := path.Join(target, input.Name)
		info, existsErr := s.files.Lstat(destination)
		if existsErr == nil {
			if !input.Replace || !info.Mode().IsRegular() {
				http.Error(w, "目标文件已存在；请确认是否替换普通文件", 409)
				return
			}
			if _, supported := s.files.HasExtension("posix-rename@openssh.com"); !supported {
				http.Error(w, "SSH 服务不支持原子替换，请使用新文件名上传", 409)
				return
			}
		} else if !os.IsNotExist(existsErr) {
			err = existsErr
			break
		}
		id := makeID() + makeID()
		temp := path.Join(target, ".remotegate-upload-"+id)
		var file *sftp.File
		file, err = s.files.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err == nil {
			upload := &remoteUpload{file: file, temp: temp, target: destination, size: input.Size, replace: input.Replace && existsErr == nil, lastOffset: -1}
			if existsErr == nil {
				upload.mode = info.Mode().Perm()
				upload.owner, _ = info.Sys().(*sftp.FileStat)
			}
			s.uploads[id] = upload
			result = map[string]any{"uploadId": id, "offset": int64(0)}
		}
	case "upload-part":
		upload := s.uploads[input.UploadID]
		if upload == nil || len(input.Data) == 0 || len(input.Data) > remoteUploadChunk {
			http.Error(w, "上传已失效或分片无效", 400)
			return
		}
		digest := sha256.Sum256(input.Data)
		if input.Offset == upload.lastOffset && len(input.Data) == upload.lastSize && digest == upload.lastHash {
			result = map[string]int64{"offset": upload.written}
			break
		}
		if input.Offset != upload.written || input.Offset+int64(len(input.Data)) > upload.size {
			http.Error(w, "上传分片位置不匹配，请重新上传", 409)
			return
		}
		var n int
		n, err = upload.file.Write(input.Data)
		if err == nil && n != len(input.Data) {
			err = io.ErrShortWrite
		}
		if err == nil {
			upload.lastOffset, upload.lastSize, upload.lastHash = input.Offset, len(input.Data), digest
			upload.written += int64(n)
			result = map[string]int64{"offset": upload.written}
		}
	case "upload-finish":
		upload := s.uploads[input.UploadID]
		if upload == nil || upload.written != upload.size {
			http.Error(w, "上传内容尚未完整接收", 409)
			return
		}
		if upload.replace {
			if upload.owner != nil {
				err = upload.file.Chown(int(upload.owner.UID), int(upload.owner.GID))
			}
			if err == nil {
				err = upload.file.Chmod(upload.mode)
			}
			if err != nil {
				http.Error(w, "无法保留原文件的所有者或权限，原文件未替换；可使用新文件名上传", 502)
				return
			}
		}
		err = upload.file.Close()
		if err == nil {
			if upload.replace {
				err = s.files.PosixRename(upload.temp, upload.target)
			} else {
				err = s.files.Rename(upload.temp, upload.target)
			}
		}
		if err == nil {
			delete(s.uploads, input.UploadID)
		}
	case "upload-cancel":
		if upload := s.uploads[input.UploadID]; upload != nil {
			_ = upload.file.Close()
			err = s.files.Remove(upload.temp)
			delete(s.uploads, input.UploadID)
		}
	default:
		http.Error(w, "未知文件操作", 400)
		return
	}
	if err != nil {
		message := "文件操作失败：" + err.Error()
		if os.IsPermission(err) {
			message = "SSH 账号没有执行该文件操作的权限"
		}
		http.Error(w, message, 502)
		return
	}
	writeJSON(w, result)
}

func (s *remoteSession) fileDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	stop := context.AfterFunc(r.Context(), func() { _ = s.conn.Close() })
	defer stop()
	idle := time.AfterFunc(60*time.Second, func() { _ = s.conn.Close() })
	defer idle.Stop()
	response := http.NewResponseController(w)
	defer response.SetWriteDeadline(time.Time{})
	s.fileMu.Lock()
	ticket := r.URL.Query().Get("ticket")
	target := s.downloads[ticket]
	delete(s.downloads, ticket)
	s.fileMu.Unlock()
	if ticket == "" || target == "" {
		http.Error(w, "下载链接已失效，请重新准备下载", 410)
		return
	}
	s.fileMu.Lock()
	if err := s.initFiles(); err != nil {
		s.fileMu.Unlock()
		http.Error(w, err.Error(), 502)
		return
	}
	file, err := s.files.Open(target)
	s.fileMu.Unlock()
	if err != nil {
		http.Error(w, "无法读取文件："+err.Error(), 502)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "文件已不可用", 409)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(target)}))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	buffer := make([]byte, 64<<10)
	for r.Context().Err() == nil {
		idle.Reset(60 * time.Second)
		n, readErr := file.Read(buffer)
		if n != 0 {
			s.touch()
			_ = response.SetWriteDeadline(time.Now().Add(60 * time.Second))
			if _, err := w.Write(buffer[:n]); err != nil {
				return
			}
		}
		if readErr != nil {
			return
		}
	}
}
