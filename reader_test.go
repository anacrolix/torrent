package torrent_test

// func TestReaderReadContext(t *testing.T) {
// 	cl, err := torrent.NewClient(torrent.TestingConfig(t))
// 	require.NoError(t, err)
// 	defer cl.Close()
// 	ts, err := torrent.NewFromMetaInfo(testutil.GreetingMetaInfo(), torrent.OptionStorage(storage.NewFile(cl.Config().DataDir)))
// 	require.NoError(t, err)
// 	tt, _, err := cl.Start(ts)
// 	require.NoError(t, err)
// 	defer cl.Stop(ts)
// 	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Millisecond))
// 	defer cancel()
// 	r := tt.Files()[0].NewReader()
// 	defer r.Close()
// 	_, err = r.ReadContext(ctx, make([]byte, 1))
// 	require.EqualValues(t, context.DeadlineExceeded, err)
// }
