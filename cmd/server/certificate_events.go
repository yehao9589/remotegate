package main

import "time"

type certificateEvent struct {
	Time    time.Time `json:"time"`
	Action  string    `json:"action"`
	Status  string    `json:"status"`
	Message string    `json:"message"`
}

func addCertificateEvent(s *certificateState, action, status, message string) {
	s.History = append(s.History, certificateEvent{Time: time.Now().UTC(), Action: action, Status: status, Message: message})
	if len(s.History) > 30 {
		s.History = append([]certificateEvent{}, s.History[len(s.History)-30:]...)
	}
}
