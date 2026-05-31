package v1

// ReportPayload is the raw JSON payload used by protocol v1 report upload.
// The agent still builds v1 reports from monitoring data dynamically so third-party
// fields can pass through without requiring a shared server dependency.
type ReportPayload = []byte
