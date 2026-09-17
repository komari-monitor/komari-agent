package rescue

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/komari-monitor/komari-agent/requestheaders"
	commonv1 "github.com/r11234567/komari-proto/gen/go/komari/common/v1"
	rescuev1 "github.com/r11234567/komari-proto/gen/go/komari/rescue/v1"
	rescuev1connect "github.com/r11234567/komari-proto/gen/go/komari/rescue/v1/rescuev1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	requestTimeout     = 30 * time.Second
	leaseTimeout       = 29 * time.Minute
	reconnectDelay     = 5 * time.Second
	maximumEventOutput = 64 << 10
	instanceFileMode   = 0o600
	instanceDirectory  = 0o700
)

type Config struct {
	Endpoint             string
	Token                string
	AgentID              string
	InstanceIDPath       string
	Version              string
	IgnoreUnsafeCert     bool
	CFAccessClientID     string
	CFAccessClientSecret string
	Action               ActionConfig
}

type Helper struct {
	client               rescuev1connect.RescueServiceClient
	token                string
	agentID              string
	instanceID           string
	version              string
	cfAccessClientID     string
	cfAccessClientSecret string
	action               ActionConfig
}

func New(config Config) (*Helper, error) {
	baseURL, err := normalizeEndpoint(config.Endpoint)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Token) == "" {
		return nil, errors.New("Agent token is required")
	}
	if (strings.TrimSpace(config.CFAccessClientID) == "") != (strings.TrimSpace(config.CFAccessClientSecret) == "") {
		return nil, errors.New("Cloudflare Access client ID and client secret must be configured together")
	}
	instancePath := strings.TrimSpace(config.InstanceIDPath)
	if instancePath == "" {
		instancePath = DefaultInstanceIDPath()
	}
	instanceID, err := loadOrCreateInstanceID(instancePath)
	if err != nil {
		return nil, fmt.Errorf("initialize helper instance ID: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = true
	// This is controlled by the explicit --ignore-unsafe-cert compatibility option.
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: config.IgnoreUnsafeCert} // #nosec G402 // lgtm[go/disabled-certificate-check]
	return &Helper{
		client: rescuev1connect.NewRescueServiceClient(&http.Client{Transport: transport}, baseURL),
		token:  strings.TrimSpace(config.Token), agentID: strings.TrimSpace(config.AgentID),
		instanceID: instanceID, version: config.Version,
		cfAccessClientID:     strings.TrimSpace(config.CFAccessClientID),
		cfAccessClientSecret: strings.TrimSpace(config.CFAccessClientSecret),
		action:               config.Action,
	}, nil
}

func DefaultInstanceIDPath() string {
	directory, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(directory) == "" {
		return ".komari-rescue-instance"
	}
	return filepath.Join(directory, "komari-agent", "rescue-instance")
}

func (h *Helper) Run(ctx context.Context) error {
	// A previous helper generation may have left a temporary SSH window open
	// whose kernel timeout or revocation timer did not survive. Reconciling at
	// startup is what makes the deadline hold across a restart rather than
	// depending on a process that is already gone.
	if err := ReconcileTemporarySSHAccess(ctx, h.action); err != nil {
		log.Printf("reconcile temporary SSH access: %v", err)
	}
	if err := h.reportStatus(ctx, nil); err != nil {
		return fmt.Errorf("report rescue helper status: %w", err)
	}
	afterAssignmentID := ""
	for {
		// Reconciling once per lease bounds how long a lost timer can leave a
		// window open to one lease interval, without any polling of its own.
		if err := ReconcileTemporarySSHAccess(ctx, h.action); err != nil {
			log.Printf("reconcile temporary SSH access: %v", err)
		}
		assignment, err := h.lease(ctx, afterAssignmentID)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			_ = h.reportStatus(ctx, err)
			if !wait(ctx, reconnectDelay) {
				return nil
			}
			continue
		}
		if assignment == nil || assignment.Session == nil {
			_ = h.reportStatus(ctx, nil)
			continue
		}
		afterAssignmentID = assignment.AssignmentId
		if err := h.execute(ctx, assignment); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			_ = h.reportStatus(ctx, err)
			if !wait(ctx, reconnectDelay) {
				return nil
			}
			continue
		}
		_ = h.reportStatus(ctx, nil)
	}
}

func (h *Helper) lease(parent context.Context, afterAssignmentID string) (*rescuev1.RescueAssignment, error) {
	ctx, cancel := context.WithTimeout(parent, leaseTimeout)
	defer cancel()
	request := connect.NewRequest(&rescuev1.LeaseRescueSessionsRequest{
		AgentId: h.agentID, HelperInstanceId: h.instanceID, AfterAssignmentId: afterAssignmentID,
	})
	h.authorize(request.Header())
	stream, err := h.client.LeaseRescueSessions(ctx, request)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	if !stream.Receive() {
		if err := stream.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	return stream.Msg().Assignment, nil
}

func (h *Helper) execute(parent context.Context, assignment *rescuev1.RescueAssignment) error {
	session := assignment.Session
	deadline := deadlineForAssignment(time.Time{})
	if assignment.LeaseExpiresAt != nil && assignment.LeaseExpiresAt.IsValid() {
		deadline = assignment.LeaseExpiresAt.AsTime()
	}
	if session.DeadlineAt != nil && session.DeadlineAt.IsValid() && session.DeadlineAt.AsTime().Before(deadline) {
		deadline = session.DeadlineAt.AsTime()
	}
	operationContext, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	cancelledByServer := make(chan struct{}, 1)
	go h.watchCancellation(operationContext, assignment.AssignmentId, cancel, cancelledByServer)

	result, actionErr := ExecuteAction(operationContext, h.action, session.Action, session.Arguments, session.GetSshPort())
	state := commonv1.OperationState_OPERATION_STATE_SUCCEEDED
	var detail *commonv1.ErrorDetail
	select {
	case <-cancelledByServer:
		state = commonv1.OperationState_OPERATION_STATE_CANCELLED
		detail = &commonv1.ErrorDetail{Code: "CANCELLED", Message: "rescue operation was cancelled by the server"}
	default:
		if errors.Is(operationContext.Err(), context.DeadlineExceeded) {
			state = commonv1.OperationState_OPERATION_STATE_DEADLINE_EXCEEDED
			detail = &commonv1.ErrorDetail{Code: "DEADLINE_EXCEEDED", Message: "rescue operation exceeded its deadline"}
		} else if actionErr != nil {
			state = commonv1.OperationState_OPERATION_STATE_FAILED
			detail = &commonv1.ErrorDetail{Code: "ACTION_FAILED", Message: truncateError(actionErr.Error())}
		}
	}
	output := finalOutput(result, state)
	event := &rescuev1.RescueEvent{
		SessionId: session.SessionId, Sequence: 1, OccurredAt: timestamppb.Now(), State: state,
		Stream: rescuev1.RescueOutputStream_RESCUE_OUTPUT_STREAM_STDOUT, Output: output, Error: detail,
	}
	// Typed results travel alongside the text output rather than replacing it,
	// so a panel that understands them renders fields while an older one still
	// shows something useful.
	if state == commonv1.OperationState_OPERATION_STATE_SUCCEEDED {
		event.Diagnostics = result.Diagnostics
		event.SshAccess = result.SSHAccess
	}
	if err := h.reportEvent(parent, event); err != nil {
		return err
	}
	if state == commonv1.OperationState_OPERATION_STATE_SUCCEEDED && result.AfterReport != nil {
		return result.AfterReport(parent)
	}
	return nil
}

func finalOutput(result ActionResult, state commonv1.OperationState) []byte {
	if state == commonv1.OperationState_OPERATION_STATE_CANCELLED || state == commonv1.OperationState_OPERATION_STATE_DEADLINE_EXCEEDED {
		return nil
	}
	output := append([]byte(nil), result.Stdout...)
	if len(result.Stderr) > 0 {
		if len(output) > 0 && output[len(output)-1] != '\n' {
			output = append(output, '\n')
		}
		output = append(output, []byte("[stderr]\n")...)
		output = append(output, result.Stderr...)
	}
	return limitBytes(output, maximumEventOutput)
}

func (h *Helper) watchCancellation(ctx context.Context, assignmentID string, cancel context.CancelFunc, cancelled chan<- struct{}) {
	assignment, err := h.lease(ctx, assignmentID)
	if err != nil || assignment == nil || assignment.Session == nil {
		return
	}
	if assignment.AssignmentId == assignmentID && assignment.Session.State == commonv1.OperationState_OPERATION_STATE_CANCEL_REQUESTED {
		select {
		case cancelled <- struct{}{}:
		default:
		}
		cancel()
	}
}

func (h *Helper) reportEvent(parent context.Context, event *rescuev1.RescueEvent) error {
	ctx, cancel := context.WithTimeout(parent, requestTimeout)
	defer cancel()
	request := connect.NewRequest(&rescuev1.ReportRescueEventRequest{
		AgentId: h.agentID, HelperInstanceId: h.instanceID, Event: event,
	})
	h.authorize(request.Header())
	response, err := h.client.ReportRescueEvent(ctx, request)
	if err != nil {
		return err
	}
	if response.Msg.AcceptedSequence < event.Sequence {
		return fmt.Errorf("server acknowledged rescue event sequence %d, expected at least %d", response.Msg.AcceptedSequence, event.Sequence)
	}
	return nil
}

func (h *Helper) reportStatus(parent context.Context, statusErr error) error {
	ctx, cancel := context.WithTimeout(parent, requestTimeout)
	defer cancel()
	status := &rescuev1.RescueHelperStatus{
		Requested: true, Installed: true, GuardianRunning: true, HelperRunning: true,
		Version:          h.version,
		HelperInstanceId: h.instanceID, ObservedAt: timestamppb.Now(),
	}
	status.NetworkIsolation, status.BlockedInterfaces = networkIsolationStatus(h.action)
	if statusErr != nil {
		status.Error = &commonv1.ErrorDetail{Code: "HELPER_CONNECTION", Message: truncateError(statusErr.Error())}
	}
	request := connect.NewRequest(&rescuev1.ReportRescueStatusRequest{AgentId: h.agentID, Status: status})
	h.authorize(request.Header())
	response, err := h.client.ReportRescueStatus(ctx, request)
	if err != nil {
		return err
	}
	if !response.Msg.Accepted {
		return errors.New("server rejected rescue helper status")
	}
	return nil
}

func (h *Helper) authorize(header http.Header) {
	requestheaders.ApplyAgentAuthentication(header, h.token, h.cfAccessClientID, h.cfAccessClientSecret)
}

func normalizeEndpoint(endpoint string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid Connect endpoint %q", endpoint)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("Connect endpoint must use HTTP or HTTPS")
	}
	return value, nil
}

func loadOrCreateInstanceID(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		value := strings.TrimSpace(string(data))
		if _, parseErr := uuid.Parse(value); parseErr != nil {
			return "", errors.New("stored helper instance ID is invalid")
		}
		return value, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), instanceDirectory); err != nil {
		return "", err
	}
	value := uuid.NewString()
	if err := atomicWrite(path, []byte(value+"\n"), instanceFileMode); err != nil {
		return "", err
	}
	return value, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, instanceDirectory); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".komari-rescue-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func truncateError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 512 {
		return value[:512]
	}
	return value
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
