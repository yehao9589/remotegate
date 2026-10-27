"""Run the actual self-extracting installer with an isolated package manager."""
import io,json,os,pathlib,struct,subprocess,sys,tarfile,tempfile
installer=pathlib.Path(sys.argv[1]).resolve()
# No real package manager is invoked and no router paths are written.
stub=r'''#!/usr/bin/env python3
import io,os,pathlib,struct,sys,tarfile
if sys.argv[1]=="print-architecture":
 print(os.environ["PACKAGE_ABI"])
elif sys.argv[1]=="install":
 with tarfile.open(sys.argv[-1],"r:gz") as outer:
  data=outer.extractfile("data.tar.gz").read()
 with tarfile.open(fileobj=io.BytesIO(data),mode="r:gz") as inner:
  binary=inner.extractfile("usr/sbin/remotegate-agent").read()
 assert struct.unpack_from("<H",binary,18)[0]==int(os.environ["ELF_MACHINE"])
 pathlib.Path(os.environ["INSTALL_MARKER"]).write_text("selected correct ABI")
else:raise SystemExit("unexpected opkg mutation")
'''
for name,abi,kernel,machine in [
 ("OpenWrt x86_64","arch all 1\narch x86_64 10","x86_64",62),
 ("ImmortalWrt ARM64","arch aarch64_cortex-a53 10","aarch64",183),
 ("iStoreOS ARM64","arch aarch64_generic 10","aarch64",183),
 ("32-bit OpenWrt-family userland with 64-bit kernel","arch arm_cortex-a53_neon-vfpv4 10","aarch64",40),
 ("unsupported MIPS userland","arch mips_24kc 10","aarch64",None),
]:
 with tempfile.TemporaryDirectory() as tmp:
  root=pathlib.Path(tmp);marker=root/"installed"
  for tool,content in {"opkg":stub,"uname":"#!/bin/sh\necho "+kernel+"\n","id":"#!/bin/sh\necho 0\n"}.items():
   p=root/tool;p.write_text(content);p.chmod(0o700)
  env=dict(os.environ,PATH=tmp+":"+os.environ["PATH"],PACKAGE_ABI=abi,ELF_MACHINE=str(machine),INSTALL_MARKER=str(marker))
  result=subprocess.run(["sh",str(installer)],env=env,capture_output=True,text=True,timeout=15)
  if machine is None:assert result.returncode!=0 and not marker.exists(),result.stdout+result.stderr
  else:assert result.returncode==0 and marker.exists(),result.stdout+result.stderr
  print("PASS actual installer: "+name)
