package spanner

import (
	"fmt"
	"slices"
	"strings"

	"github.com/alis-build/protog/diff"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// GetDatabaseDdl returns membership and descriptors from the same schema
// snapshot. File names, source locations and language-generation options are
// not type identity: Spanner can combine files into synthetic package files.
type bundleSnapshot struct {
	Types       map[string]struct{}
	Descriptors []byte
}

func removeUnchangedUpdates(plan *diff.Diff, currentBytes, desiredBytes []byte) error {
	if len(plan.Update) == 0 {
		return nil
	}
	current, err := descriptorTypes(currentBytes)
	if err != nil {
		return fmt.Errorf("reading stored proto descriptors: %w", err)
	}
	desired, err := descriptorTypes(desiredBytes)
	if err != nil {
		return fmt.Errorf("reading desired proto descriptors: %w", err)
	}
	updates := make([]string, 0, len(plan.Update))
	for _, name := range plan.Update {
		old, oldOK := current[name]
		want, wantOK := desired[name]
		if !oldOK || !wantOK {
			// Fail closed rather than guessing that missing descriptors are equal
			// or submitting an update for every type in the package.
			return fmt.Errorf("cannot compare proto type %q: descriptor missing (stored=%t, desired=%t)", name, oldOK, wantOK)
		}
		if proto.Equal(old, want) {
			plan.Ignore = append(plan.Ignore, name)
		} else {
			updates = append(updates, name)
		}
	}
	plan.Update = updates
	slices.Sort(plan.Ignore)
	return nil
}

func descriptorTypes(data []byte) (map[string]proto.Message, error) {
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(data, set); err != nil {
		return nil, err
	}
	types := make(map[string]proto.Message)
	add := func(name string, value proto.Message) error {
		if _, exists := types[name]; exists {
			return fmt.Errorf("duplicate proto type %q", name)
		}
		types[name] = value
		return nil
	}
	var addMessage func(string, *descriptorpb.DescriptorProto) error
	addMessage = func(prefix string, message *descriptorpb.DescriptorProto) error {
		name := prefix + message.GetName()
		if err := add(name, message); err != nil {
			return err
		}
		for _, nested := range message.GetNestedType() {
			if err := addMessage(name+".", nested); err != nil {
				return err
			}
		}
		for _, enum := range message.GetEnumType() {
			if err := add(name+"."+enum.GetName(), enum); err != nil {
				return err
			}
		}
		return nil
	}
	for _, file := range set.GetFile() {
		// Editions require resolving inherited feature settings; until that is
		// supported, do not risk classifying them as an unchanged proto2 type.
		if file.GetSyntax() == "editions" {
			return nil, fmt.Errorf("descriptor comparison does not yet support editions: %s", file.GetName())
		}
		prefix := file.GetPackage()
		if prefix != "" {
			prefix += "."
		}
		for _, message := range file.GetMessageType() {
			normalizeMessage(message, file.GetSyntax() == "proto3")
			if err := addMessage(prefix, message); err != nil {
				return nil, err
			}
		}
		for _, enum := range file.GetEnumType() {
			if err := add(prefix+enum.GetName(), enum); err != nil {
				return nil, err
			}
		}
	}
	return types, nil
}

func normalizeMessage(message *descriptorpb.DescriptorProto, proto3 bool) {
	// Spanner orders nested declarations by name. Their source order has no
	// effect on field numbers, references, or enum default values.
	slices.SortFunc(message.NestedType, func(a, b *descriptorpb.DescriptorProto) int {
		return strings.Compare(a.GetName(), b.GetName())
	})
	slices.SortFunc(message.EnumType, func(a, b *descriptorpb.EnumDescriptorProto) int {
		return strings.Compare(a.GetName(), b.GetName())
	})
	// Stored descriptors represent proto3 optional fields as ordinary proto2
	// optional fields, without the compiler's synthetic oneof. Keep real oneofs
	// (and their mutual-exclusion semantics), remapping their indexes as needed.
	synthetic := make(map[int32]bool)
	for _, field := range message.GetField() {
		if field.GetProto3Optional() && field.OneofIndex != nil {
			synthetic[field.GetOneofIndex()] = true
		}
	}
	indexes := make(map[int32]int32)
	oneofs := make([]*descriptorpb.OneofDescriptorProto, 0, len(message.OneofDecl))
	for i, oneof := range message.OneofDecl {
		if !synthetic[int32(i)] {
			indexes[int32(i)] = int32(len(oneofs))
			oneofs = append(oneofs, oneof)
		}
	}
	message.OneofDecl = oneofs
	for _, field := range message.GetField() {
		if field.GetProto3Optional() {
			field.OneofIndex = nil
			field.Proto3Optional = nil
		} else if field.OneofIndex != nil {
			if index, ok := indexes[field.GetOneofIndex()]; ok {
				field.OneofIndex = proto.Int32(index)
			}
		}
		if field.GetTypeName() != "" {
			field.TypeName = proto.String("." + strings.TrimPrefix(field.GetTypeName(), "."))
		}
		// Spanner materializes the proto3 packed default in its proto2-style
		// stored descriptors. Preserve explicit false and all other options.
		if field.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED && packable(field.GetType()) {
			packed := proto3
			if field.GetOptions() != nil && field.Options.Packed != nil {
				packed = field.Options.GetPacked()
			}
			if field.Options == nil {
				field.Options = &descriptorpb.FieldOptions{}
			}
			field.Options.Packed = proto.Bool(packed)
		}
	}
	for _, nested := range message.GetNestedType() {
		normalizeMessage(nested, proto3)
	}
}

func packable(kind descriptorpb.FieldDescriptorProto_Type) bool {
	return kind != descriptorpb.FieldDescriptorProto_TYPE_MESSAGE &&
		kind != descriptorpb.FieldDescriptorProto_TYPE_GROUP &&
		kind != descriptorpb.FieldDescriptorProto_TYPE_STRING &&
		kind != descriptorpb.FieldDescriptorProto_TYPE_BYTES
}
