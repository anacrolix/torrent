package torrent

type peerNetworks struct {
	tcp4, tcp6 bool
	utp4, utp6 bool
}

func handleErr(h func(), fs ...func() error) error {
	for _, f := range fs {
		err := f()
		if err != nil {
			h()
			return err
		}
	}
	return nil
}
