// From https://github.com/jackpal/Taipei-Torrent

// Copyright (c) 2010 Jack Palevich. All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are
// met:
//
//    * Redistributions of source code must retain the above copyright
// notice, this list of conditions and the following disclaimer.
//    * Redistributions in binary form must reproduce the above
// copyright notice, this list of conditions and the following disclaimer
// in the documentation and/or other materials provided with the
// distribution.
//    * Neither the name of Google Inc. nor the names of its
// contributors may be used to endorse or promote products derived from
// this software without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
// OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
// SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
// LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
// DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
// THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

package metainfo

// For more context on why these numbers, see http://wiki.vuze.com/w/Torrent_Piece_Size
const (
	minimumPieceLength   = 16 * 1024
	targetPieceCountLog2 = 10
	targetPieceCountMin  = 1 << targetPieceCountLog2
)

// Target piece count should be < targetPieceCountMax
const targetPieceCountMax = targetPieceCountMin << 1

// Choose a good piecelength.
func ChoosePieceLength(totalLength int64) (pieceLength int64) {
	return ChoosePieceLengthEx(ChoosePieceLengthOpts{TotalLength: totalLength})
}

type ChoosePieceLengthOpts struct {
	TotalLength       int64
	MinPieceSize      int64
	MaxPieceSize      int64
	SoftMinPieceCount int64
	SoftMaxPieceCount int64
}

// Choose a good piece length within the given bounds. Starting from MinPieceSize, the piece
// length is doubled (halving the piece count) until the count drops below SoftMaxPieceCount,
// staying within MaxPieceSize and not taking the count below SoftMinPieceCount. The hard size
// bounds win over the soft count targets. Any zero-valued option assumes its default; MaxPieceSize
// defaults to no upper bound.
func ChoosePieceLengthEx(opts ChoosePieceLengthOpts) (pieceLength int64) {
	if opts.MinPieceSize == 0 {
		opts.MinPieceSize = minimumPieceLength
	}
	if opts.SoftMaxPieceCount == 0 {
		opts.SoftMaxPieceCount = targetPieceCountMax
	}
	if opts.SoftMinPieceCount == 0 {
		opts.SoftMinPieceCount = opts.SoftMaxPieceCount / 2
	}
	pieceLength = opts.MinPieceSize
	pieces := opts.TotalLength / pieceLength
	for pieces >= opts.SoftMaxPieceCount {
		if opts.MaxPieceSize != 0 && pieceLength<<1 > opts.MaxPieceSize {
			break
		}
		if opts.SoftMinPieceCount != 0 && pieces>>1 < opts.SoftMinPieceCount {
			break
		}
		pieceLength <<= 1
		pieces >>= 1
	}
	return
}
