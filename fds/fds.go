// Package fds provides helper methods for parsing fds files
package fds

import (
	"errors"
	"fmt"
	"os"

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
	cmd.AddCommand(viewCmd())
	return cmd
}

func viewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "types",
		Short: "View all messages and enums in a fds file",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return errors.New("expecting exactly one argument for the path to the fds file")
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			types, err := ParseFdsTypes(args[0])
			if err != nil {
				alog.Fatalf(cmd.Context(), "parsing fds types: %v", err)
			}
			for t := range types {
				println(t)
			}
		},
	}
	return cmd
}

func ParseFdsTypes(filePath string) (map[string]struct{}, error) {
	fds, err := ParseFds(filePath)
	if err != nil {
		return nil, err
	}
	// We'll use the protokit to simplify the handling of the fds file.
	fdsFiles := []string{}
	for _, f := range fds.GetFile() {
		fdsFiles = append(fdsFiles, f.GetName())
	}
	fileDescriptors := protokit.ParseCodeGenRequest(&plugin.CodeGeneratorRequest{
		FileToGenerate: fdsFiles,
		ProtoFile:      fds.GetFile(),
	})

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
	return types, nil
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

func ParseFds(filePath string) (*descriptorpb.FileDescriptorSet, error) {
	fileBytes, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", filePath, err)
	}
	fds := descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(fileBytes, &fds); err != nil {
		return nil, fmt.Errorf("unmarshalling %s: %w", filePath, err)
	}
	return &fds, nil
}
