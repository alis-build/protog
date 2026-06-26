protoc: 
	protoc --descriptor_set_out=test/fds -I=. test/test.proto

types:
	go run . fds types test/fds

events:
	go run . fds events test/fds

