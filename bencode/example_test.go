package bencode_test

import (
	"fmt"
	"log"

	"github.com/anacrolix/torrent/bencode"
)

func Example() {
	type Message struct {
		Query string `bencode:"q,omitempty"`
	}

	v := Message{Query: "ping"}

	// Encode
	data, err := bencode.Marshal(v)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("encoded: %s\n", data)

	// Decode
	var decoded Message
	err = bencode.Unmarshal(data, &decoded)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("decoded: %+v\n", decoded)
	// Output:
	// encoded: d1:q4:pinge
	// decoded: {Query:ping}
}
