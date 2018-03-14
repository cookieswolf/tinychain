package p2p

import (
	"github.com/libp2p/go-libp2p-peer"
	ma "github.com/multiformats/go-multiaddr"
	libnet "github.com/libp2p/go-libp2p-net"
	"fmt"

	"tinychain/p2p/pb"
	"errors"
	"time"
	"github.com/golang/protobuf/proto"
)

var (
	routeSyncTimeout = 45 * time.Second
	normalTimeout    = 30 * time.Second
	okTimeout        = 30 * time.Second
)

type Stream struct {
	remoteId   peer.ID       // Remote peer id
	remoteAddr ma.Multiaddr  // Remote peer multiaddr
	stream     libnet.Stream // Stream between peer and remote peer
	peer       *Peer         // Local peer
	//handshake  bool          // Id handshake successful or not

	//handshakeSuccessCh chan struct{} // Channel when handshake successfully
	pbChan      chan *pb.Message // Channel for message transfering
	quitWriteCh chan struct{}    // Channel for quiting
}

func NewStreamWithPid(pid peer.ID, peer *Peer) *Stream {
	return NewStream(pid, nil, nil, peer)
}

func NewStream(pid peer.ID, addr ma.Multiaddr, stream libnet.Stream, peer *Peer) *Stream {
	return &Stream{
		remoteId:   pid,
		remoteAddr: addr,
		stream:     stream,
		peer:       peer,
		//handshake:   false,
		pbChan:      make(chan *pb.Message, 2*1024),
		quitWriteCh: make(chan struct{}, 1),
	}
}

// Connect to remote peer
func (s *Stream) Connect() error {
	stream, err := s.peer.host.NewStream(
		s.peer.context,
		s.remoteId,
		TransProtocol,
	)
	if err != nil {
		log.Infof("Failed to connect remote peer %s:%s\n", s.remoteId.Pretty(), err)
		return err
	}
	s.stream = stream
	s.remoteAddr = stream.Conn().RemoteMultiaddr()
	//log.Infof("Connect to Peer. Info: %s\n", s.remoteAddr)

	s.StartLoop()

	return nil
}

// Check is handshake successful or not
//func (s *Stream) Handshake() bool {
//	return s.handshake
//}

func (s *Stream) String() string {
	addrStr := ""
	if s.remoteAddr != nil {
		addrStr = s.remoteAddr.String()
	}
	return fmt.Sprintf("Peer Stream:%s,%s\n", s.remoteId.Pretty(), addrStr)
}

func (s *Stream) Close(reason error) {
	//log.Info("Closing stream.")

	// Clean up
	//s.peer.Streams.Remove(s.remoteId)

	// Quit write channel
	//s.quitWriteCh <- struct{}{}

	if s.stream != nil {
		s.stream.Close()
		s.stream = nil
	}
}

func (s *Stream) StartLoop() {
	//log.Infof("Stream to %s starts loop\n", s.remoteId)
	//go s.writeLoop()
	go s.readLoop()
}

func (s *Stream) SendMessage(name string, data interface{}) error {
	if s.stream == nil {
		if err := s.Connect(); err != nil {
			return err
		}
	}
	var (
		message *pb.Message
		err     error
	)
	switch data.(type) {
	case *pb.PeerData:
		message, err = pb.NewPeerDataMsg(name, data.(*pb.PeerData))
	case *pb.NormalData:
		message, err = pb.NewNormalMsg(name, data.(*pb.NormalData))
	default:
		log.Error("Invalid data type of message")
		return errors.New("Invalid data type of message")
	}
	if err != nil {
		log.Infof("Failed to send message %s\n", name)
		return err
	}

	// Set deadline
	s.SetReadDeadline(name)

	// Write data to stream
	seri, _ := message.Serialize()
	_, err = s.stream.Write(seri)
	if err != nil {
		log.Infof("Failed to send message to peer %s. Message name:%s",
			s.remoteAddr, message.Name)
		return err
	}
	return nil
}

func (s *Stream) SetReadDeadline(name string) {
	if s.stream == nil {
		return
	}
	switch name {
	case pb.ROUTESYNC_REQ:
		fallthrough
	case pb.ROUTESYNC_RESP:
		s.stream.SetReadDeadline(time.Now().Add(routeSyncTimeout))
	case pb.NORMAL_MSG:
		s.stream.SetReadDeadline(time.Now().Add(normalTimeout))
	case pb.OK_MSG:
		s.stream.SetReadDeadline(time.Now().Add(okTimeout))
	}
}

// Write message to stream
//func (s *Stream) WriteMessage(message *pb.Message) error {
//	data, _ := message.Serialize()
//	_, err := s.stream.Write(data)
//	if err != nil {
//		log.Infof("Failed to send message to peer %s. Message name:%s",
//			s.remoteAddr, message.Name)
//		return err
//	}
//	return nil
//}

func (s *Stream) readLoop() {
	if s.stream == nil {
		if err := s.Connect(); err != nil {
			s.Close(err)
			return
		}
	}

	var (
		message *pb.Message
		dataLen uint32
		buf     = make([]byte, 1024*4)
		msgBuf  = make([]byte, 1024)
	)

	for {
		n, err := s.stream.Read(buf)
		if err != nil {
			s.Close(err)
			//log.Infof("Stream Close. %s.\n", err)
			return
		}
		msgBuf = append(msgBuf, buf[:n]...)

		if dataLen == 0 {
			if uint32(len(msgBuf)) < pb.DATA_LENGTH_SIZE {
				continue
			}
			dataLen, err = pb.BytesToUint32(msgBuf[:pb.DATA_LENGTH_SIZE])
			if err != nil {
				log.Fatalf("Failed to read data length:%s\n", err)
				break
			}
		}
		// Reading data is not enough
		if uint32(len(msgBuf))-pb.DATA_LENGTH_SIZE < dataLen {
			continue
		}

		message, err = pb.DeserializeMsg(msgBuf)
		if err != nil {
			log.Fatalf("Failed to deserialize message:%s\n", err)
			break
		}
		err = s.handleMsg(message)
		if err != nil {
			log.Info(err)
		}
		return
	}
}

//func (s *Stream) writeLoop() {
//	//handshakeTimeout := time.NewTicker(30 * time.Second)
//	//select {
//	//case <-handshakeTimeout.C:
//	//	// handshake timeout
//	//	return
//	//case <-s.handshakeSuccessCh:
//	//}
//
//	for {
//		select {
//		case <-s.quitWriteCh:
//			log.Info("Quit stream write loop")
//			return
//		case pb := <-s.pbChan:
//			s.WriteMessage(pb)
//		}
//	}
//}

// Handle message coming from remote peer
func (s *Stream) handleMsg(message *pb.Message) error {
	// Discover and update remote peer in local route table
	s.peer.routeTable.AddPeer(s.remoteId, s.remoteAddr)

	// Notice peer
	go func() { s.peer.respCh <- message }()

	// Handle message
	pbName := message.Name
	log.Infof("Peer %s receive pb `%s`\n", s.peer.ID(), pbName)
	switch pbName {
	case pb.OK_MSG:
		// success response
		s.Close(nil)
	case pb.ROUTESYNC_REQ:
		// A peer wants your route table
		return s.onSyncRoute()
	case pb.ROUTESYNC_RESP:
		s.Close(nil)
		// Update local route table
		return s.syncRoute(message.Data)
	case pb.NORMAL_MSG:
		log.Infof("Message content: %s\n", message.Data)
		s.Close(nil)
		return nil
	}
	log.Infof("Invalid message name %s.\n", pbName)
	return errors.New("invalid message name")

}

// Sync route request handler
func (s *Stream) onSyncRoute() error {
	// Get nearest peers from route table
	peers := s.peer.routeTable.GetNearestPeers(s.remoteId)

	peerInfos := make([]*pb.PeerInfo, len(peers))
	for i, v := range peers {
		pinfo := &pb.PeerInfo{
			Id:    v.ID.Pretty(),
			Addrs: make([]string, len(v.Addrs)),
		}
		for j, addr := range v.Addrs {
			pinfo.Addrs[j] = addr.String()
		}
		peerInfos[i] = pinfo
	}
	peerData := &pb.PeerData{peerInfos}
	return s.SendMessage(pb.ROUTESYNC_RESP, peerData)
}

// Receive `ROUTESYNC_RESP` and Update local route table
func (s *Stream) syncRoute(data []byte) error {
	peerData := &pb.PeerData{}
	err := proto.Unmarshal(data, peerData)
	if err != nil {
		log.Errorf("Failed to unmarshal bytes to peer data")
		return err
	}
	return s.peer.routeTable.AddPeers(peerData.Peers)
}