This document describes copper frames types, their purpose and interactions.

## Big Endian

Similar to HTTP/2 copper uses big-endian byte ordering.

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

* Measuring latency between endpoints
* Establishing causality between processed frames

Causality is established with a simple fact: to see a PING request the endpoint has to receive and process all preceding frames first, thus receiving a PING response proves that all frames sent before that request have been processed by the other side.

PING frames support the following flags:

| Flag | Description |
| ---- | ----------- |
| <nobr>ACK (bit 0)</nobr> | When set this frame is a PING response, otherwise it is a PING request |

Header MUST have `stream_id` set to 0.

Payload MUST be exactly 8 bytes with the following format:

| Field | Size | Description |
| ----- | ---- | ----------- |
| data | <nobr>8 bytes</nobr> | Arbitrary 8 bytes of data which MUST be resent verbatim in a PING response |

## DATA frames

DATA frames are used for opening new streams and sending data on those streams. When open new streams an appropriate stream id must be chosen with the following rule:

* Clients use odd numbers, greater than 0 and less than 2^31
* Servers use even numbers, greater than 0 and less than 2^31

When choosing stream id it is important to be sure it is not still being used by the other side of the connection. The general rule is that stream ids are available for reuse when both sides of the stream have been closed and causality is established with PING. Correct reuse of stream ids is very important, since for example with only 1000 new streams per second 31-bit stream ids (effectively 30-bit since only odd or even numbers may be used by each side) would be exhausted in less than 13 days. Beware, that currently clients make an assumption that on wrap around old stream ids are so old that they don't require establishing a causality (for example at a burn rate of 150k rps stream id space might become exhausted in 4 hours, however that is plenty of time for older streams to send their final window updates and reset frames).

DATA frames support the following flags:

| Flag | Description |
| ---- | ----------- |
| <nobr>EOF (bit 0)</nobr> | When set further data will not be sent on the stream |
| <nobr>OPEN (bit 1)</nobr> | When set this frame opens a new stream |
| <nobr>ACK (bit 2)</nobr> | When set this frame acknowledges the stream |

Setting both OPEN and EOF flags immediately half-closes the connection, which is useful for RPC with small request sizes.

Setting ACK flag is akin to sending `100 Continue` in HTTP: the client may wish to hold off on sending additional data until the server proves that it is willing to receive it and that it won't immediately fail. While the use of stream acknowledgements is optional in the core protocol, client and server must negotiate separately whether its use is mandatory between them. One example would be the copper rpc, where acknowledgements are handled automatically and transparently for the user.

Payload MUST NOT exceed available space in the endpoint stream window.

Sending DATA frames without an OPEN flag set is only allowed when the stream is open, sending DATA frames after EOF is not permitted.

A special exception is an empty DATA frame with stream id and flags set to zero. Such frames MUST be ignored and are used as probes for keeping connection alive.

## RESET frames

RESET frames are used for two purposes:

* Informing the other side that all received data will be discarded
* Conveying an error that will be returned by remote's read and write calls

RESET frames support the following flags:

| Flag | Description |
| ---- | ----------- |
| <nobr>READ (bit 0)</nobr> | When set the endpoint will ignore further data on the stream |
| <nobr>WRITE (bit 1)</nobr> | When set the endpoint will not send further data on the stream (similar to EOF) |

Payload MUST be at least 4 bytes with the following format:

| Field | Type | Description |
| ----- | ---- | ----------- |
| code | int32_t | a 32-bit error code |
| message | ... | the rest of payload is an optional error message |

After receiving RESET endpoints SHOULD stop sending further data on the stream and SHOULD schedule a frame with EOF flag set (unless such frame has already been sent). Sending any more data after receiving RESET is pointless, the remote is likely to discard it.

Sending RESET with a READ flag closes the read side of the connection and instructs the remote to fail pending write calls with the specified error code. Read calls on the other side SHOULD NOT be affected.

Sending RESET with a WRITE flag closes the write side of the connection and instructs the remote to fail final read calls with the specified error code. Write calls on the other side SHOULD NOT be affected.

A special error code ECLOSED is used when stream sides are closed normally. When error needs to be returned from a read call as a result of a RESET frame with this error code it SHOULD be converted to an equivalent of a EOF.

## WINDOW frames

WINDOW frames are used for telling endpoints how much more data may be sent on the stream. Special stream id 0 is used for updating receive window for the connection, otherwise update is for receive window for the specified stream.

Endpoints MUST NOT send more data on each stream than both connection and stream windows permit. Only payload in OPEN and DATA frames are counted towards the window, all other frames and their data are not considered. Note that sending data on the stream removes space from both connection and stream windows.

WINDOW frames do not support any flags.

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
* OPEN (before sending or after receiving a DATA frame with OPEN flag)
* HALF-CLOSED
  * READ
    * after receiving DATA with EOF flag set
    * after receiving RESET with WRITE flag set
    * (tentatively) after sending RESET with READ flag set
  * WRITE
    * after sending DATA with EOF flag set
    * after sending RESET with WRITE flag set
    * (tentatively) after receiving RESET with READ flag set
* FULLY-CLOSED
  * both READ and WRITE are HALF-CLOSED
  * may be reused after causality is proven

Rules on what may be sent and received with a given stream id are as follows:

* After sending OPEN all other frames may be sent and received
* After receiving a RESET frame with READ flag set:
  * SHOULD NOT send additional DATA frames with data
  * SHOULD fail pending write calls with an attached error
* After receiving a DATA frame with EOF flag set:
  * HALF-CLOSED(READ) is added to stream state
  * SHOULD NOT send additional WINDOW frames
  * SHOULD prefer sending RESET after all DATA frames
  * SHOULD prefer sending DATA with EOF instead of RESET with ECLOSED
* After receiving a RESET frame with WRITE flag set:
  * SHOULD treat it like DATA frame with EOF flag set
  * SHOULD fail final read calls with an attached error
* After sending a DATA frame with EOF flag set:
  * HALF-CLOSED(WRITE) is added to stream state
  * MUST NOT send additional DATA frames
* After reaching FULLY-CLOSED state:
  * MUST NOT send additional frames
