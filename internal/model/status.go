package model

import "strings"

// SourceStatus is the recorded outcome of one adapter fetch.
type SourceStatus string

// Adapter outcomes.
const (
	SourceSuccess SourceStatus = "success"
	SourcePartial SourceStatus = "partial"
	SourceFailure SourceStatus = "failure"
)

// ParseSourceStatus normalizes a stored source status.
func ParseSourceStatus(raw string) SourceStatus {
	switch SourceStatus(strings.ToLower(strings.TrimSpace(raw))) {
	case SourceSuccess:
		return SourceSuccess
	case SourcePartial:
		return SourcePartial
	default:
		return SourceFailure
	}
}

// OK reports whether the source produced usable data.
func (s SourceStatus) OK() bool {
	return s == SourceSuccess || s == SourcePartial
}

// NotificationState is the lifecycle of an outbox row.
type NotificationState string

// Outbox states.
const (
	NotificationPending NotificationState = "pending"
	NotificationSent    NotificationState = "sent"
	NotificationFailed  NotificationState = "failed"
)
