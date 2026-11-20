package mailbox

import (
	"encoding/binary"
	"errors"
)

// ChunkSize is the transfer block size (below the frame cap of proto.MaxPayload).
const ChunkSize = 256 << 10

// CapDrop is the capability the hub announces in its Hello when it serves the mailbox.
const CapDrop = "drop"

// JSON payloads of the mailbox frames.
type (
	// PutBeginReq starts or resumes an upload (always to the inbox).
	PutBeginReq struct {
		Item Item `json:"item"`
	}
	// PutBeginResp says where to continue from.
	PutBeginResp struct {
		Offset   int64 `json:"offset"`
		Complete bool  `json:"complete"`
	}
	// ChunkAck acknowledges what was received so far.
	ChunkAck struct {
		Offset int64 `json:"offset"`
	}
	// ListReq asks for the items of a box.
	ListReq struct {
		Box Box `json:"box"`
	}
	// ListResp lists items.
	ListResp struct {
		Items []Item `json:"items"`
	}
	// GetReq asks for an item from an offset (resume).
	GetReq struct {
		Box    Box    `json:"box"`
		ID     string `json:"id"`
		Offset int64  `json:"offset"`
	}
	// DeleteReq deletes an item already received.
	DeleteReq struct {
		Box Box    `json:"box"`
		ID  string `json:"id"`
	}
)

// encodeChunk builds a block's binary payload: [offset u64 BE][data].
func encodeChunk(offset int64, data []byte) []byte {
	b := make([]byte, 8+len(data))
	binary.BigEndian.PutUint64(b, uint64(offset))
	copy(b[8:], data)
	return b
}

func decodeChunk(p []byte) (int64, []byte, error) {
	if len(p) < 8 {
		return 0, nil, errors.New("block without a header")
	}
	return int64(binary.BigEndian.Uint64(p)), p[8:], nil
}
