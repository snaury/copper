This document describes copper frames types, their purpose and interactions.

## Frame header

Every frame starts with a 9 byte header:

| Field | Size | Description |
| ----- | ---- | ----------- |
| stream_id | <nobr>4 bytes</nobr> | 31-bit unsigned stream id |
| payload_size | <nobr>3 bytes</nobr> | 24-bit unsigned payload size |
| frame_flags | <nobr>1 byte</nobr> | 8-bit flags |
| frame_type | <nobr>1 byte</nobr> | 8-bit frame type |

Immediately after the header are `payload_size` bytes of payload.

## PING frames

PING frames are the highest priority frames that have two primary uses:

* Establishing causality between processed frames
* Measuring latency between endpoints

Causality is established with a simple fact: to see a PING request endpoint has to receive and process all preceding frames first, thus receiving a PING response proves that all frames sent before that request have been processed by the other side.

PING frames support the following flags:

| Flag | Description |
| ---- | ----------- |
| <nobr>ACK (bit 0)</nobr> | When set this frame is a PING response, otherwise it is a PING request |

Header MUST have `stream_id` set to 0.

Payload MUST be exactly 8 bytes with the following format:

| Field | Size | Description |
| ----- | ---- | ----------- |
| data | <nobr>8 bytes</nobr> | Arbitrary 8 bytes of data which MUST be resent verbatim in a PING response |

## OPEN frames

OPEN frames are used for opening new streams, where stream id is chosen with the following rule:

* Clients use odd numbers
* Servers use even numbers

When choosing stream id it is important to be sure it is not still being used by the other side of the connection. The general rule is that stream ids are available for reuse when both sides of the stream have been closed and causality is established with PING. Correct reuse of stream ids is very important, since for example with only 1000 new streams per second 31-bit stream ids (effectively 30-bit since only odd or even numbers may be used by each side) would be exhausted in less than 13 days.

OPEN frames support the following flags:

| Flag | Description |
| ---- | ----------- |
| <nobr>FIN (bit 0)</nobr> | When set further data will not be sent on the stream |

Setting FIN flag immediately half-closes the connection, which is useful for RPC with small request sizes.

Payload MUST be at least 8 bytes with the following format:

| Field | Type | Description |
| ----- | ---- | ----------- |
| target_id | int64_t | Either statically prearranged or negotiated destination for the stream |
| data | ... | Data may be included in the frame (MUST NOT exceed remote stream window size) |

## DATA frames

DATA frames are used for sending additional data on the stream and support the following flags:

| Flag | Description |
| ---- | ----------- |
| <nobr>FIN (bit 0)</nobr> | When set further data will not be sent on the stream |

Setting FIN flag immediately half-closes the connection.

Payload contains optional data (MUST NOT exceed available space in the remote stream window).

Sending DATA frames is only allowed when the stream is open and there is enough space in the remote stream window.

## RESET frames

RESET frames are used for two purposes:

* Informing the other side that all received data will be discarded
* Conveying an error that will be returned by remote's read and write calls

RESET frames support the following flags:

| Flag | Description |
| ---- | ----------- |
| <nobr>FIN (bit 0)</nobr> | When set further data will not be sent on the stream |

Payload MUST be at least 4 bytes with the following format:

| Field | Type | Description |
| ----- | ---- | ----------- |
| code | int32_t | a 32-bit error code |
| message | ... | the rest of payload is an optional error message |

After receiving RESET endpoints SHOULD stop sending further data on the stream and schedule a frame with FIN flag set (unless such frame has already been sent).

Sending RESET with a FIN flag is akin to forceful stream termination: no further data will be sent, all incoming data will be discarded. Additional effect of RESET with FIN flag set is it ensures read calls on the other side will return an error related to the specified error code.

A special error code ESTREAMCLOSED is used when stream is closed normally. When error needs to be returned from a read call as a result of a RESET frame such error should be converted to an equivalent of a EOF.

## WINDOW frames

WINDOW frames are used for telling endpoints how much more data may be sent on the stream. Special stream id 0 is used for updating receive window for the connection, otherwise update is for receive window of the specified stream.

Endpoints MUST NOT send more data on each stream than both connection and stream windows permit. Only data in OPEN (minus 8 bytes for target) and DATA frames is counted towards the window, all other frames and their data are not considered. Note that sending data on the stream removes space from both connection and stream windows.

WINDOW frames support the following flags:

| Flag | Description |
| ---- | ----------- |
| <nobr>ACK (bit 0)</nobr> | When set this frame acknowledges removal of the data from the stream's receive buffer, otherwise it temporarily allows more data to be sent |

Payload MUST be exactly 4 bytes with the following format:

| Field | Type | Description |
| ----- | ---- | ----------- |
| increment | uint32_t | An unsigned window increment, MUST be greater than zero |

## SETTINGS frames

TODO

## Stream states

Support for predictable stream id reuse necessitates very strict rules on which frames may be sent and received at any given time.

Each stream has the following high level states:

* FREE
* OPEN (before sending or after receiving an OPEN frame)
* HALF-CLOSED
  * READ: after receiving a frame with FIN flag set
  * WRITE: after sending a frame with FIN flag set
* FULLY-CLOSED
  * sent all acknowledgments
  * received all acknowledgements
  * both READ and WRITE are HALF-CLOSED
* DEAD (may not be reused until causality is proven)

Rules on what may be sent and received with a given stream id are as follows:

* After sending OPEN all other frames may be sent and received
* After receiving a RESET frame:
  * SHOULD fail pending write calls with an attached error
  * SHOULD fail final read calls with an attached error
  * SHOULD NOT send any DATA frames
* After sending a frame with FIN flag set:
  * HALF-CLOSED(READ) must be added to stream state
  * MUST NOT send any DATA frames
* After receiving a frame with FIN flag set:
  * HALF-CLOSED(WRITE) must be added to stream state
  * SHOULD prefer sending RESET after all DATA frames
  * SHOULD prefer sending DATA with FIN instead of RESET with ESTREAMCLOSED
* After reaching FULLY-CLOSED state:
  * MUST NOT send any additional frames
  * MUST move stream to DEAD state
