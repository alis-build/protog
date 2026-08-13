// Package fds provides helper methods for parsing fds files
package fds

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	plugin "github.com/golang/protobuf/protoc-gen-go/plugin"
	"github.com/pseudomuto/protokit"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/alis-build/protog/output"
)

func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fds",
		Short: "Perform parsing operations on fds files",
	}
	cmd.AddCommand(typesCmd())
	cmd.AddCommand(eventsCmd())
	return cmd
}

type typesResult struct {
	Path  string   `json:"path"`
	Types []string `json:"types"`
}

func (r typesResult) Lines() []string { return r.Types }

type eventsResult struct {
	Path   string   `json:"path"`
	Events []string `json:"events"`
}

func (r eventsResult) Lines() []string { return r.Events }

func typesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "types",
		Short: "View messages and enums",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return errors.New("expecting exactly one argument for the path to the fds file")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			types, _, err := ParseFdsTypes(args[0])
			if err != nil {
				return err
			}
			output.Result(typesResult{Path: args[0], Types: slices.Sorted(maps.Keys(types))})
			return nil
		},
	}
	return cmd
}

func eventsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "View top-level messages ending with 'Event'",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return errors.New("expecting exactly one argument for the path to the fds file")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			events, _, err := ParseEvents(args[0])
			if err != nil {
				return err
			}
			output.Result(eventsResult{Path: args[0], Events: slices.Sorted(maps.Keys(events))})
			return nil
		},
	}
	return cmd
}

func ParseEvents(filePath string) (map[string]struct{}, []byte, error) {
	fileDescriptors, fdsBytes, err := ParseFds(filePath)
	if err != nil {
		return nil, nil, err
	}
	events := map[string]struct{}{}
	for _, file := range fileDescriptors {
		for _, message := range file.GetMessages() {
			if strings.HasSuffix(message.GetFullName(), "Event") {
				events[message.GetFullName()] = struct{}{}
			}
		}
	}
	output.FdsParsed(filePath, 0, len(events))
	return events, fdsBytes, nil
}

func ParseFdsTypes(filePath string) (map[string]struct{}, []byte, error) {
	fileDescriptors, fdsBytes, err := ParseFds(filePath)
	if err != nil {
		return nil, nil, err
	}
	types := map[string]struct{}{}
	for _, file := range fileDescriptors {
		for _, enum := range file.GetEnums() {
			types[enum.GetFullName()] = struct{}{}
		}
		for _, fileMessage := range file.GetMessages() {
			for _, message := range Messages(fileMessage) {
				types[message.GetFullName()] = struct{}{}
			}
			for _, enum := range Enums(fileMessage) {
				types[enum.GetFullName()] = struct{}{}
			}
		}
	}
	output.FdsParsed(filePath, len(types), 0)
	return types, fdsBytes, nil
}

// Enums is a recursive method which fetches all the underlyging enums from each message.
func Enums(message *protokit.Descriptor) []*protokit.EnumDescriptor {
	enums := []*protokit.EnumDescriptor{}
	enums = append(enums, message.GetEnums()...)
	for _, nested := range message.Messages {
		enums = append(enums, Enums(nested)...)
	}
	return enums
}

// Messages is a recursive method which fetches all the underlyging messages from each message.
func Messages(message *protokit.Descriptor) []*protokit.Descriptor {
	messages := []*protokit.Descriptor{message}
	// If the provided message has any sub messages defined, add them.
	messages = append(messages, message.GetMessages()...)

	// Iterate through the nested messages.
	for _, nested := range message.GetMessages() {
		messages = append(messages, Messages(nested)...)
	}
	return messages
}

// ParseFds parses a fds file and returns the FileDescriptorSet and the raw bytes.
func ParseFds(filePath string) ([]*protokit.FileDescriptor, []byte, error) {
	fileBytes, err := os.ReadFile(filePath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", filePath, err)
	}
	fds := descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(fileBytes, &fds); err != nil {
		return nil, nil, fmt.Errorf("unmarshalling %s: %w", filePath, err)
	}
	fdsFiles := []string{}
	for _, f := range fds.GetFile() {
		fdsFiles = append(fdsFiles, f.GetName())
	}
	return protokit.ParseCodeGenRequest(&plugin.CodeGeneratorRequest{
		FileToGenerate: fdsFiles,
		ProtoFile:      fds.GetFile(),
	}), fileBytes, nil
}
