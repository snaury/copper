package copper

import (
	"encoding/binary"
	"io"

	"github.com/golang/protobuf/proto"
	"github.com/snaury/copper/protocol"
)

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
		return EINVALID
	}
	return proto.Unmarshal(data, pb)
}

func rpcWriteRequestType(w io.Writer, rtype protocol.RequestType) error {
	var buf [1]byte
	buf[0] = uint8(rtype)
	_, err := w.Write(buf[0:1])
	return err
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

func rpcSimpleRequest(conn RawConn, targetID int64, rtype protocol.RequestType, request proto.Message, response proto.Message) error {
	stream, err := conn.Open(targetID)
	if err != nil {
		return err
	}
	defer stream.Close()
	err = rpcWriteRequestType(stream, rtype)
	if err != nil {
		return err
	}
	err = rpcWriteMessage(stream, request)
	if err != nil {
		return err
	}
	err = stream.CloseWrite()
	if err != nil {
		return err
	}
	err = rpcReadMessage(stream, response)
	if err != nil {
		return err
	}
	return nil
}

func rpcStreamingRequest(conn RawConn, targetID int64, rtype protocol.RequestType, request proto.Message) (Stream, error) {
	stream, err := conn.Open(targetID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if stream != nil {
			stream.Close()
		}
	}()
	err = rpcWriteRequestType(stream, rtype)
	if err != nil {
		return nil, err
	}
	err = rpcWriteMessage(stream, request)
	if err != nil {
		return nil, err
	}
	err = stream.CloseWrite()
	if err != nil {
		return nil, err
	}
	result := stream
	stream = nil
	return result, nil
}
