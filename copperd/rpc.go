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

func rpcReadRequestType(r io.Reader) (rtype protocol.RequestType, err error) {
	var buf [1]byte
	_, err = io.ReadFull(r, buf[0:1])
	if err != nil {
		return
	}
	return protocol.RequestType(buf[0]), nil
}

func rpcReadMessage(r io.Reader, pb proto.Message) error {
	var buf [4]byte
	_, err := io.ReadFull(r, buf[0:4])
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	size := int(binary.LittleEndian.Uint32(buf[0:4]))
	var data []byte
	if size > 0 {
		data = make([]byte, size)
		_, err = io.ReadFull(r, data)
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return err
		}
	} else if size < 0 {
		return copper.EINVALIDDATA
	}
	return proto.Unmarshal(data, pb)
}

func rpcWriteMessage(w io.Writer, pb proto.Message) error {
	data, err := proto.Marshal(pb)
	if err != nil {
		return err
	}
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(data)))
	_, err = w.Write(buf[0:4])
	if err != nil {
		return err
	}
	if len(data) > 0 {
		_, err = w.Write(data)
		if err != nil {
			return err
		}
	}
	return nil
}

func rpcWrap(stream copper.Stream, server lowLevelServer) error {
	rtype, err := rpcReadRequestType(stream)
	if err != nil {
		return copper.EINVALIDDATA
	}
	switch rtype {
	case protocol.RequestType_Subscribe:
		var request protocol.SubscribeRequest
		err = rpcReadMessage(stream, &request)
		if err != nil {
			return copper.EINVALIDDATA
		}
		var options []SubscribeOption
		for _, poption := range request.GetOptions() {
			options = append(options, SubscribeOption{
				Service:    poption.GetService(),
				Distance:   poption.GetDistance(),
				MaxRetries: poption.GetMaxRetries(),
			})
		}
		targetID, err := server.subscribe(options...)
		if err != nil {
			return err
		}
		return rpcWriteMessage(stream, &protocol.SubscribeResponse{
			TargetId: proto.Int64(targetID),
		})
	case protocol.RequestType_GetEndpoints:
		var request protocol.GetEndpointsRequest
		err = rpcReadMessage(stream, &request)
		if err != nil {
			return copper.EINVALIDDATA
		}
		endpoints, err := server.getEndpoints(request.GetTargetId())
		if err != nil {
			return err
		}
		var pendpoints []*protocol.Endpoint
		for _, endpoint := range endpoints {
			pendpoint := &protocol.Endpoint{
				Address: proto.String(endpoint.Address),
			}
			if len(endpoint.Network) > 0 {
				pendpoint.Network = proto.String(endpoint.Network)
			}
			pendpoints = append(pendpoints, pendpoint)
		}
		return rpcWriteMessage(stream, &protocol.GetEndpointsResponse{
			Endpoints: pendpoints,
		})
	}
	panic("TODO")
}
