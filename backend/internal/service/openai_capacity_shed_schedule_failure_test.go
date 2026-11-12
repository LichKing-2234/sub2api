package service

import (
	"net/http"
	"testing"
)

const capacityShedEventBody = `{"type":"error","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later.","type":"service_unavailable_error"},"sequence_number":2}`

func TestShouldReportAccountScheduleFailure_CapacityShedIsRequestScoped(t *testing.T) {
	err := &UpstreamFailoverError{
		StatusCode:             http.StatusServiceUnavailable,
		ResponseBody:           []byte(capacityShedEventBody),
		RetryableOnSameAccount: true,
		RequestScopedTransient: true,
	}
	if !err.IsOpenAICapacityShed() {
		t.Fatalf("precondition failed: test body was not recognized as capacity shed")
	}
	if err.ShouldReportAccountScheduleFailure() {
		t.Fatal("capacity shed must not be counted as an account scheduling failure")
	}
}

func TestShouldReportAccountScheduleFailure_NonCapacityUpstreamErrorStillReported(t *testing.T) {
	err := &UpstreamFailoverError{
		StatusCode:   http.StatusBadGateway,
		ResponseBody: []byte(`{"error":{"message":"bad gateway"}}`),
	}
	if err.ShouldReportAccountScheduleFailure() != true {
		t.Fatal("ordinary upstream errors must keep their scheduler-health behavior")
	}
}

func TestShouldReportAccountScheduleFailure_CredentialScopeAccountStillReported(t *testing.T) {
	err := &UpstreamFailoverError{
		Stage: GatewayFailureStageAccountAuth,
		Scope: GatewayFailureScopeAccount,
	}
	if !err.ShouldReportAccountScheduleFailure() {
		t.Fatal("credential failures scoped to the account must still be reported")
	}
}

func TestShouldReportAccountScheduleFailure_NilReceiver(t *testing.T) {
	var err *UpstreamFailoverError
	if err.ShouldReportAccountScheduleFailure() {
		t.Fatal("nil failover error must not be reported")
	}
}
