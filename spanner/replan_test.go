package spanner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// The scenario behind these tests: several neurons of one product share a
// Spanner database and import the same well-known type (google.type.DayOfWeek).
// Deployed at the same time, each reads a bundle without the type, each plans
// an INSERT for it, and only the first DDL succeeds; the rest fail with
// "AlreadyExists: Type already exists: google.type.DayOfWeek".

func dayOfWeekFile() *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name: proto.String("google/type/dayofweek.proto"), Package: proto.String("google.type"), Syntax: proto.String("proto3"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{Name: proto.String("DayOfWeek"), Value: []*descriptorpb.EnumValueDescriptorProto{
			{Name: proto.String("DAY_OF_WEEK_UNSPECIFIED"), Number: proto.Int32(0)},
			{Name: proto.String("MONDAY"), Number: proto.Int32(1)},
		}}},
	}
}

func reportsFile() *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name: proto.String("reports.proto"), Package: proto.String("vistaequity.mr.reports.v1"), Syntax: proto.String("proto3"),
		Dependency: []string{"google/type/dayofweek.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Report"), Field: []*descriptorpb.FieldDescriptorProto{{
			Name: proto.String("day"), JsonName: proto.String("day"), Number: proto.Int32(1),
			Type: descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(), TypeName: proto.String(".google.type.DayOfWeek"),
		}}}},
	}
}

func names(types ...string) map[string]struct{} {
	m := map[string]struct{}{}
	for _, t := range types {
		m[t] = struct{}{}
	}
	return m
}

// concurrentDeployPlan is the reports neuron's plan, read while the bundle
// held only the files neuron's own types.
func concurrentDeployPlan(t *testing.T) *Plan {
	t.Helper()
	desired := descriptorBytes(t, dayOfWeekFile(), reportsFile())
	plan := &Plan{
		Database:   "projects/p/instances/i/databases/d",
		FdsBytes:   desired,
		desired:    names("google.type.DayOfWeek", "vistaequity.mr.reports.v1.Report"),
		packageIDs: []string{"vistaequity.mr.reports.v1"},
	}
	before := &bundleSnapshot{Types: names("vistaequity.mr.files.v1.File")}
	if err := plan.replan(before); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Create, []string{"google.type.DayOfWeek", "vistaequity.mr.reports.v1.Report"}) {
		t.Fatalf("initial plan = %+v", plan.Diff)
	}
	return plan
}

func TestDeployReplansWhenAnotherDeployInsertedTheSharedType(t *testing.T) {
	plan := concurrentDeployPlan(t)
	// After the files neuron's DDL landed, the bundle holds the shared enum
	// with the same descriptor the reports neuron ships.
	after := &bundleSnapshot{
		Types:       names("vistaequity.mr.files.v1.File", "google.type.DayOfWeek"),
		Descriptors: descriptorBytes(t, dayOfWeekFile()),
	}
	snapshots := 0
	plan.snapshot = func(context.Context) (*bundleSnapshot, error) {
		snapshots++
		return after, nil
	}
	var statements []string
	plan.apply = func(_ context.Context, statement string, _ time.Time) (*LroInfo, error) {
		statements = append(statements, statement)
		if strings.Contains(statement, "INSERT(`google.type.DayOfWeek`") {
			return &LroInfo{Name: "operations/1"}, fmt.Errorf("waiting for Spanner Database DDL update to complete: %w",
				status.Error(codes.AlreadyExists, "Type already exists: google.type.DayOfWeek"))
		}
		return &LroInfo{Name: "operations/2", Done: true}, nil
	}
	info, err := plan.Deploy(context.Background())
	if err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}
	if info == nil || !info.Done || info.Name != "operations/2" {
		t.Fatalf("Deploy() = %+v", info)
	}
	if snapshots != 1 {
		t.Fatalf("bundle re-read %d times, want 1", snapshots)
	}
	want := []string{
		"ALTER PROTO BUNDLE INSERT(`google.type.DayOfWeek`,`vistaequity.mr.reports.v1.Report`)",
		"ALTER PROTO BUNDLE INSERT(`vistaequity.mr.reports.v1.Report`)",
	}
	if !slices.Equal(statements, want) {
		t.Fatalf("statements = %q, want %q", statements, want)
	}
	// The shared type is outside the package and unchanged: ignored, not
	// updated, exactly as a sequential deploy would have planned it.
	if !slices.Equal(plan.Ignore, []string{"google.type.DayOfWeek", "vistaequity.mr.files.v1.File"}) || len(plan.Update) != 0 {
		t.Fatalf("replanned diff = %+v", plan.Diff)
	}
}

func TestDeployReplansToNoopWhenEverythingLandedMeanwhile(t *testing.T) {
	plan := concurrentDeployPlan(t)
	// A reports deploy retried by hand while an identical one was already
	// applying: on re-read every type is present and unchanged.
	after := &bundleSnapshot{
		Types:       names("vistaequity.mr.files.v1.File", "google.type.DayOfWeek", "vistaequity.mr.reports.v1.Report"),
		Descriptors: descriptorBytes(t, dayOfWeekFile(), reportsFile()),
	}
	plan.snapshot = func(context.Context) (*bundleSnapshot, error) { return after, nil }
	applies := 0
	plan.apply = func(context.Context, string, time.Time) (*LroInfo, error) {
		applies++
		return nil, fmt.Errorf("updating Spanner Database DDL: %w", status.Error(codes.AlreadyExists, "Type already exists: google.type.DayOfWeek"))
	}
	info, err := plan.Deploy(context.Background())
	if err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}
	if info == nil || !info.Done || info.Name != "operations/none" {
		t.Fatalf("Deploy() = %+v", info)
	}
	if applies != 1 {
		t.Fatalf("DDL submitted %d times, want 1", applies)
	}
	if plan.Statement() != "" {
		t.Fatalf("replanned statement = %q, want none", plan.Statement())
	}
}

func TestDeployDoesNotReplanOtherErrors(t *testing.T) {
	plan := concurrentDeployPlan(t)
	plan.snapshot = func(context.Context) (*bundleSnapshot, error) {
		t.Fatal("bundle must not be re-read for an unrelated failure")
		return nil, nil
	}
	wantErr := status.Error(codes.PermissionDenied, "caller lacks spanner.databases.updateDdl")
	applies := 0
	plan.apply = func(context.Context, string, time.Time) (*LroInfo, error) {
		applies++
		return &LroInfo{Name: "operations/1"}, wantErr
	}
	info, err := plan.Deploy(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Deploy() error = %v, want %v", err, wantErr)
	}
	if info == nil || info.Name != "operations/1" || applies != 1 {
		t.Fatalf("Deploy() = %+v after %d applies", info, applies)
	}
}

func TestDeployGivesUpAfterMaxReplans(t *testing.T) {
	plan := concurrentDeployPlan(t)
	// The bundle never shows the type, so every re-plan inserts it again.
	before := &bundleSnapshot{Types: names("vistaequity.mr.files.v1.File")}
	snapshots := 0
	plan.snapshot = func(context.Context) (*bundleSnapshot, error) {
		snapshots++
		return before, nil
	}
	applies := 0
	plan.apply = func(context.Context, string, time.Time) (*LroInfo, error) {
		applies++
		return &LroInfo{Name: "operations/1"}, status.Error(codes.AlreadyExists, "Type already exists: google.type.DayOfWeek")
	}
	_, err := plan.Deploy(context.Background())
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("Deploy() error = %v, want AlreadyExists", err)
	}
	if applies != maxReplans+1 || snapshots != maxReplans {
		t.Fatalf("applies = %d, snapshots = %d; want %d, %d", applies, snapshots, maxReplans+1, maxReplans)
	}
}

func TestDeployReportsRefreshFailure(t *testing.T) {
	plan := concurrentDeployPlan(t)
	refreshErr := errors.New("spanner.GetDatabaseDdl: unavailable")
	plan.snapshot = func(context.Context) (*bundleSnapshot, error) { return nil, refreshErr }
	plan.apply = func(context.Context, string, time.Time) (*LroInfo, error) {
		return &LroInfo{Name: "operations/1"}, status.Error(codes.AlreadyExists, "Type already exists: google.type.DayOfWeek")
	}
	_, err := plan.Deploy(context.Background())
	if !errors.Is(err, refreshErr) || !strings.Contains(err.Error(), "Type already exists") {
		t.Fatalf("Deploy() error = %v, want both the DDL and the refresh failure", err)
	}
}

func TestStaleBundleClassification(t *testing.T) {
	for _, err := range []error{
		status.Error(codes.AlreadyExists, "Type already exists: google.type.DayOfWeek"),
		fmt.Errorf("waiting: %w", status.Error(codes.AlreadyExists, "Type already exists: google.type.Month")),
		status.Error(codes.NotFound, "Type not found: vistaequity.mr.files.v1.File"),
		fmt.Errorf("waiting: %w", status.Error(codes.NotFound, "Type not found: vistaequity.mr.files.v1.File")),
		errors.New("rpc error: Type already exists: google.type.DayOfWeek"),
	} {
		if !staleBundle(err) {
			t.Errorf("staleBundle(%v) = false, want true", err)
		}
	}
	for _, err := range []error{
		status.Error(codes.PermissionDenied, "denied"),
		status.Error(codes.InvalidArgument, "Syntax error"),
		errors.New("network is unreachable"),
		errors.New("Type not found: vistaequity.mr.files.v1.File"),
	} {
		if staleBundle(err) {
			t.Errorf("staleBundle(%v) = true, want false", err)
		}
	}
}

func TestDestroyReplanDeletesOnlyRequestedPackages(t *testing.T) {
	plan := &Plan{Database: "projects/p/instances/i/databases/d", Destroy: true, packageIDs: []string{"vistaequity.mr.reports.v1"}}
	if err := plan.replan(&bundleSnapshot{Types: names("vistaequity.mr.reports.v1.Report", "google.type.DayOfWeek")}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Delete, []string{"vistaequity.mr.reports.v1.Report"}) || !slices.Equal(plan.Ignore, []string{"google.type.DayOfWeek"}) {
		t.Fatalf("destroy plan = %+v", plan.Diff)
	}
	if plan.ExistingTypes != 2 {
		t.Fatalf("ExistingTypes = %d, want 2", plan.ExistingTypes)
	}
}
