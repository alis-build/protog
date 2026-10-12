package spanner

import (
	"context"
	"slices"
	"testing"

	"github.com/alis-build/protog/diff"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func descriptorBytes(t *testing.T, files ...*descriptorpb.FileDescriptorProto) []byte {
	t.Helper()
	data, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: files})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testDescriptorFile() *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name: proto.String("events.proto"), Package: proto.String("test.events"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Event"), Field: []*descriptorpb.FieldDescriptorProto{{
			Name: proto.String("numbers"), JsonName: proto.String("numbers"), Number: proto.Int32(1),
			Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT64.Enum(),
		}}}},
	}
}

func TestUnchangedDescriptorSkipsDDLDespiteFileMetadataAndPackedDefault(t *testing.T) {
	desired := testDescriptorFile()
	desired.Options = &descriptorpb.FileOptions{GoPackage: proto.String("example/events")}
	desired.SourceCodeInfo = &descriptorpb.SourceCodeInfo{Location: []*descriptorpb.SourceCodeInfo_Location{{LeadingComments: proto.String("new comment")}}}
	stored := proto.Clone(desired).(*descriptorpb.FileDescriptorProto)
	stored.Name = proto.String("package:test.events")
	stored.Syntax, stored.Options, stored.SourceCodeInfo = nil, nil, nil
	stored.MessageType[0].Field[0].Options = &descriptorpb.FieldOptions{Packed: proto.Bool(true)}
	plan := &Plan{ExistingTypes: 1, Diff: &diff.Diff{Update: []string{"test.events.Event"}}}
	if err := removeUnchangedUpdates(plan.Diff, descriptorBytes(t, stored), descriptorBytes(t, desired)); err != nil {
		t.Fatal(err)
	}
	if len(plan.Update) != 0 || !slices.Equal(plan.Ignore, []string{"test.events.Event"}) {
		t.Fatalf("plan = %+v", plan.Diff)
	}
	if got := plan.Statement(); got != "" {
		t.Fatalf("no-op DDL = %q", got)
	}
	// A nil admin client proves that a no-op deploy makes no API call.
	info, err := plan.Deploy(context.Background())
	if err != nil || info == nil || !info.Done || info.Name != "operations/none" {
		t.Fatalf("Deploy() = %+v, %v", info, err)
	}
}

func TestDescriptorDiffKeepsChangedNestedAndEnumTypes(t *testing.T) {
	stored := testDescriptorFile()
	stored.MessageType[0].NestedType = []*descriptorpb.DescriptorProto{{Name: proto.String("Detail")}}
	stored.MessageType[0].EnumType = []*descriptorpb.EnumDescriptorProto{{Name: proto.String("State"), Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String("UNKNOWN"), Number: proto.Int32(0)}}}}
	stored.MessageType = append(stored.MessageType, &descriptorpb.DescriptorProto{Name: proto.String("Unchanged")})
	desired := proto.Clone(stored).(*descriptorpb.FileDescriptorProto)
	desired.MessageType[0].NestedType[0].Field = []*descriptorpb.FieldDescriptorProto{{Name: proto.String("value"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()}}
	desired.MessageType[0].EnumType[0].Value = append(desired.MessageType[0].EnumType[0].Value, &descriptorpb.EnumValueDescriptorProto{Name: proto.String("READY"), Number: proto.Int32(1)})
	plan := &diff.Diff{Create: []string{"test.events.New"}, Delete: []string{"test.events.Removed"}, Update: []string{"test.events.Event", "test.events.Event.Detail", "test.events.Event.State", "test.events.Unchanged"}}
	if err := removeUnchangedUpdates(plan, descriptorBytes(t, stored), descriptorBytes(t, desired)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Update, []string{"test.events.Event", "test.events.Event.Detail", "test.events.Event.State"}) {
		t.Fatalf("updates = %v", plan.Update)
	}
	if len(plan.Create) != 1 || len(plan.Delete) != 1 {
		t.Fatalf("membership changes lost: %+v", plan)
	}
}

func TestDescriptorDiffPreservesExplicitPackedAndFieldChanges(t *testing.T) {
	for _, mutate := range []func(*descriptorpb.FieldDescriptorProto){
		func(f *descriptorpb.FieldDescriptorProto) {
			f.Options = &descriptorpb.FieldOptions{Packed: proto.Bool(false)}
		},
		func(f *descriptorpb.FieldDescriptorProto) {
			f.Type = descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
		},
		func(f *descriptorpb.FieldDescriptorProto) { f.Number = proto.Int32(2) },
		func(f *descriptorpb.FieldDescriptorProto) {
			f.Options = &descriptorpb.FieldOptions{Deprecated: proto.Bool(true)}
		},
	} {
		stored, desired := testDescriptorFile(), testDescriptorFile()
		mutate(desired.MessageType[0].Field[0])
		plan := &diff.Diff{Update: []string{"test.events.Event"}}
		if err := removeUnchangedUpdates(plan, descriptorBytes(t, stored), descriptorBytes(t, desired)); err != nil {
			t.Fatal(err)
		}
		if len(plan.Update) != 1 {
			t.Fatal("real field change was skipped")
		}
	}
}

func TestDescriptorDiffFailsClosedWithoutComparableDescriptors(t *testing.T) {
	valid := descriptorBytes(t, testDescriptorFile())
	for _, data := range [][]byte{nil, []byte("invalid"), descriptorBytes(t, &descriptorpb.FileDescriptorProto{})} {
		if err := removeUnchangedUpdates(&diff.Diff{Update: []string{"test.events.Event"}}, data, valid); err == nil {
			t.Fatal("missing or invalid stored descriptors should fail")
		}
	}
	editions := testDescriptorFile()
	editions.Syntax = proto.String("editions")
	if err := removeUnchangedUpdates(&diff.Diff{Update: []string{"test.events.Event"}}, valid, descriptorBytes(t, editions)); err == nil {
		t.Fatal("unresolved edition feature defaults should fail")
	}
}

func TestDescriptorDiffNormalizesSyntheticOptionalOneofsOnly(t *testing.T) {
	desired := testDescriptorFile()
	m := desired.MessageType[0]
	m.OneofDecl = []*descriptorpb.OneofDescriptorProto{{Name: proto.String("_value")}, {Name: proto.String("choice")}}
	m.Field = []*descriptorpb.FieldDescriptorProto{
		{Name: proto.String("value"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), OneofIndex: proto.Int32(0), Proto3Optional: proto.Bool(true)},
		{Name: proto.String("choice_a"), Number: proto.Int32(2), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), OneofIndex: proto.Int32(1)},
		{Name: proto.String("choice_b"), Number: proto.Int32(3), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), OneofIndex: proto.Int32(1)},
	}
	stored := proto.Clone(desired).(*descriptorpb.FileDescriptorProto)
	stored.Syntax = nil
	stored.MessageType[0].OneofDecl = stored.MessageType[0].OneofDecl[1:]
	stored.MessageType[0].Field[0].OneofIndex, stored.MessageType[0].Field[0].Proto3Optional = nil, nil
	for _, field := range stored.MessageType[0].Field[1:] {
		field.OneofIndex = proto.Int32(0)
	}
	plan := &diff.Diff{Update: []string{"test.events.Event"}}
	if err := removeUnchangedUpdates(plan, descriptorBytes(t, stored), descriptorBytes(t, desired)); err != nil {
		t.Fatal(err)
	}
	if len(plan.Update) != 0 {
		t.Fatal("synthetic optional oneof should not trigger DDL")
	}
	desired.MessageType[0].Field[1].OneofIndex = nil
	plan.Update = []string{"test.events.Event"}
	if err := removeUnchangedUpdates(plan, descriptorBytes(t, stored), descriptorBytes(t, desired)); err != nil {
		t.Fatal(err)
	}
	if len(plan.Update) != 1 {
		t.Fatal("real oneof membership change must trigger DDL")
	}
}

func TestDescriptorDiffIgnoresNestedDeclarationOrder(t *testing.T) {
	desired := testDescriptorFile()
	desired.MessageType[0].NestedType = []*descriptorpb.DescriptorProto{{Name: proto.String("Z")}, {Name: proto.String("A")}}
	desired.MessageType[0].EnumType = []*descriptorpb.EnumDescriptorProto{{Name: proto.String("ZState")}, {Name: proto.String("AState")}}
	stored := proto.Clone(desired).(*descriptorpb.FileDescriptorProto)
	slices.Reverse(stored.MessageType[0].NestedType)
	slices.Reverse(stored.MessageType[0].EnumType)
	plan := &diff.Diff{Update: []string{"test.events.Event"}}
	if err := removeUnchangedUpdates(plan, descriptorBytes(t, stored), descriptorBytes(t, desired)); err != nil {
		t.Fatal(err)
	}
	if len(plan.Update) != 0 {
		t.Fatal("nested declaration order should not trigger DDL")
	}
}

func TestDescriptorDiffRetainsPackageScope(t *testing.T) {
	stored := testDescriptorFile()
	desired := testDescriptorFile()
	desired.MessageType[0].Field[0].Number = proto.Int32(2)
	imported := proto.Clone(stored).(*descriptorpb.FileDescriptorProto)
	imported.Package = proto.String("other.events")
	importedChange := proto.Clone(imported).(*descriptorpb.FileDescriptorProto)
	importedChange.MessageType[0].Field[0].Number = proto.Int32(2)
	names := map[string]struct{}{"test.events.Event": {}, "other.events.Event": {}}
	plan := diff.New(names, names, []string{"test.events"})
	if err := removeUnchangedUpdates(plan, descriptorBytes(t, stored, imported), descriptorBytes(t, desired, importedChange)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Update, []string{"test.events.Event"}) || !slices.Equal(plan.Ignore, []string{"other.events.Event"}) {
		t.Fatalf("package scope changed: %+v", plan)
	}
}
