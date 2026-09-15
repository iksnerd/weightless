package client

import "weightless/internal/torrent"

// FileEntry aliases the shared torrent metadata type, so the client, the
// torrent parser, and the CLI all describe a file the same way.
type FileEntry = torrent.FileEntry

// TorrentMeta holds the metadata needed for downloading. It embeds the parsed
// torrent metadata and adds the v1 info hash, which the client needs for
// announces and peer handshakes but which isn't a field of a parsed info dict.
type TorrentMeta struct {
	torrent.TorrentMeta
	InfoHashV1 []byte
}
