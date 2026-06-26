// Package fds provides helper methods for parsing fds files
package fds

import (
	"context"
	"errors"
	"os"
	"strings"

	plugin "github.com/golang/protobuf/protoc-gen-go/plugin"
	"github.com/pseudomuto/protokit"
	"github.com/spf13/cobra"
	"go.alis.build/alog"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
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
		Run: func(cmd *cobra.Command, args []string) {
			types, _ := ParseFdsTypes(args[0])
			for t := range types {
				println(t)
			}
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
		Run: func(cmd *cobra.Command, args []string) {
			events, _ := ParseEvents(args[0])
			for t := range events {
				println(t)
			}
		},
	}
	return cmd
}

func ParseEvents(filePath string) (map[string]struct{}, []byte) {
	fileDescriptors, fdsBytes := ParseFds(filePath)
	events := map[string]struct{}{}
	for _, file := range fileDescriptors {
		for _, message := range file.GetMessages() {
			if strings.HasSuffix(message.GetFullName(), "Event") {
				events[message.GetFullName()] = struct{}{}
			}
		}
	}
	return events, fdsBytes
}

func ParseFdsTypes(filePath string) (map[string]struct{}, []byte) {
	fileDescriptors, fdsBytes := ParseFds(filePath)
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
	return types, fdsBytes
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
func ParseFds(filePath string) ([]*protokit.FileDescriptor, []byte) {
	println("Parsing fds file " + filePath)
	fileBytes, err := os.ReadFile(filePath)
	if err != nil {
		alog.Alertf(context.Background(), "reading %s: %v", filePath, err)
	}
	fds := descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(fileBytes, &fds); err != nil {
		alog.Alertf(context.Background(), "unmarshalling %s: %v", filePath, err)
	}
	fdsFiles := []string{}
	for _, f := range fds.GetFile() {
		fdsFiles = append(fdsFiles, f.GetName())
	}
	return protokit.ParseCodeGenRequest(&plugin.CodeGeneratorRequest{
		FileToGenerate: fdsFiles,
		ProtoFile:      fds.GetFile(),
	}), fileBytes
}
