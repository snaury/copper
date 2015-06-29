package copperd

import (
	"encoding/binary"
	"fmt"
	"github.com/golang/protobuf/proto"
	"github.com/snaury/copper"
	"github.com/snaury/copper/copperd/protocol"
	"io"
)

type rpcError struct {
	code copper.ErrorCode
}

func (e rpcError) Error() string {
	return fmt.Sprintf("TODO: error code %d", e.code)
}

func (e rpcError) ErrorCode() copper.ErrorCode {
	return e.code
}

type rpcHeader struct {
	size int
	kind protocol.Command
}

func rpcReadHeader(r io.Reader) (hdr rpcHeader, err error) {
	var buf [5]byte
	_, err = io.ReadFull(r, buf[0:5])
	if err != nil {
		return
	}
	hdr.size = int(binary.LittleEndian.Uint32(buf[0:4]))
	hdr.kind = protocol.Command(buf[4])
	return
}

func rpcReadMessage(r io.Reader, size int, m proto.Message) error {
	var buf []byte
	if size > 0 {
		buf := make([]byte, size)
		_, err := io.ReadFull(r, buf)
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return err
		}
	}
	return proto.Unmarshal(buf, m)
}

func rpcWriteHeader(w io.Writer, hdr rpcHeader) (err error) {
	var buf [5]byte
	binary.LittleEndian.PutUint32(buf[0:4], uint32(hdr.size))
	buf[4] = uint8(hdr.kind)
	_, err = w.Write(buf[0:5])
	return
}

func rpcWriteMessage(w io.Writer, kind protocol.Command, m proto.Message) error {
	buf, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	err = rpcWriteHeader(w, rpcHeader{
		size: len(buf),
		kind: kind,
	})
	if err != nil {
		return err
	}
	_, err = w.Write(buf)
	if err != nil {
		return err
	}
	return nil
}

type rpcGetEndpointsResponse interface {
	Read() (protocol.GetEndpointsResponse, error)
	Close() error
}

type rpcService interface {
	Subscribe(protocol.SubscribeRequest) (protocol.SubscribeResponse, error)
	GetEndpoints(protocol.GetEndpointsRequest) (rpcGetEndpointsResponse, error)
	Unsubscribe(protocol.UnsubscribeRequest) (protocol.UnsubscribeResponse, error)
	Publish(protocol.PublishRequest) (protocol.PublishResponse, error)
	Unpublish(protocol.UnpublishRequest) (protocol.UnpublishResponse, error)
	SetRoute(protocol.SetRouteRequest) (protocol.SetRouteResponse, error)
	LookupRoute(protocol.LookupRouteRequest) (protocol.LookupRouteResponse, error)
}

func rpcWrap(stream copper.Stream, service rpcService) error {
	hdr, err := rpcReadHeader(stream)
	if err != nil {
		return copper.EINVALIDDATA
	}
	switch hdr.kind {
	case protocol.Command_Subscribe:
		var request protocol.SubscribeRequest
		err = rpcReadMessage(stream, hdr.size, &request)
		if err != nil {
			return copper.EINVALIDDATA
		}
		result, err := service.Subscribe(request)
		if err != nil {
			return err
		}
		return rpcWriteMessage(stream, protocol.Command_Subscribe, &result)
	case protocol.Command_GetEndpoints:
		var request protocol.GetEndpointsRequest
		err = rpcReadMessage(stream, hdr.size, &request)
		if err != nil {
			return copper.EINVALIDDATA
		}
		results, err := service.GetEndpoints(request)
		if err != nil {
			return err
		}
		for {
			result, err := results.Read()
			if err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
			err = rpcWriteMessage(stream, protocol.Command_GetEndpoints, &result)
			if err != nil {
				return err
			}
		}
	}
	panic("TODO")
}
