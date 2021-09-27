package metainfo

type Metadata struct {
	CoverUrl    string   `bencode:"cover url,omitempty"`
	Description string   `bencode:"description,omitempty"`
	TagList     []string `bencode:"taglist,omitempty"`
	Title       string   `bencode:"title,omitempty"`
}
