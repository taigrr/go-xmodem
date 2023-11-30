package xmodem

import (
	"bytes"
	"io"

	"github.com/taigrr/log-socket/log"
)

const (
	NUL  byte = 0x00
	SOH  byte = 0x01
	STX  byte = 0x02
	EOT  byte = 0x04
	SUB  byte = 0x1A
	ACK  byte = 0x06
	NAK  byte = 0x15
	POLL byte = 0x43
)

const (
	XModemBlockLength   = 128
	XModem1KBlockLength = 1024
)

func CRC16(data []byte) uint16 {
	var u16CRC uint16 = 0

	for _, character := range data {
		part := uint16(character)

		u16CRC = u16CRC ^ (part << 8)
		for i := 0; i < 8; i++ {
			if u16CRC&0x8000 > 0 {
				u16CRC = u16CRC<<1 ^ 0x1021
			} else {
				u16CRC = u16CRC << 1
			}
		}
	}

	return u16CRC
}

func CRC16Constant(data []byte, length int) uint16 {
	var u16CRC uint16 = 0

	for _, character := range data {
		part := uint16(character)

		u16CRC = u16CRC ^ (part << 8)
		for i := 0; i < 8; i++ {
			if u16CRC&0x8000 > 0 {
				u16CRC = u16CRC<<1 ^ 0x1021
			} else {
				u16CRC = u16CRC << 1
			}
		}
	}

	for c := 0; c < length-len(data); c++ {
		u16CRC = u16CRC ^ (uint16(SUB) << 8)
		for i := 0; i < 8; i++ {
			if u16CRC&0x8000 > 0 {
				u16CRC = u16CRC<<1 ^ 0x1021
			} else {
				u16CRC = u16CRC << 1
			}
		}
	}

	return u16CRC
}

func sendBlock(c io.ReadWriter, block uint8, data []byte, packetPayloadLen int) error {
	startByte := SOH
	if packetPayloadLen == XModem1KBlockLength {
		startByte = STX
	}
	// send start byte
	if _, err := c.Write([]byte{startByte}); err != nil {
		return err
	}
	if _, err := c.Write([]byte{block}); err != nil {
		return err
	}
	if _, err := c.Write([]byte{^block}); err != nil {
		return err
	}

	// send data
	var toSend bytes.Buffer
	toSend.Write(data)
	for toSend.Len() < packetPayloadLen {
		toSend.Write([]byte{SUB})
	}

	paddedData := toSend.Bytes()
	if _, err := c.Write(paddedData); err != nil {
		return err
	}

	// calc CRC
	u16CRC := CRC16Constant(data, packetPayloadLen)

	// send CRC
	if _, err := c.Write([]byte{uint8(u16CRC >> 8)}); err != nil {
		return err
	}
	if _, err := c.Write([]byte{uint8(u16CRC & 0xFF)}); err != nil {
		return err
	}

	return nil
}

func ModemSend(c io.ReadWriter, data []byte) error {
	return modemSend(c, data, XModemBlockLength)
}

func ModemSend1K(c io.ReadWriter, data []byte) error {
	return modemSend(c, data, XModem1KBlockLength)
}

func modemSend(c io.ReadWriter, data []byte, packetPayloadLen int) error {
	oBuffer := make([]byte, 1)

	if _, err := c.Read(oBuffer); err != nil {
		return err
	}

	// Start Connection
	if oBuffer[0] == POLL {
		blocks := len(data) / packetPayloadLen
		if len(data) > blocks*packetPayloadLen {
			blocks++
		}

		failed := 0
		currentBlock := 0
		for currentBlock < blocks && failed < 10 {
			if int(int(currentBlock+1)*int(packetPayloadLen)) > len(data) {
				err := sendBlock(c, uint8((currentBlock+1)%256), data[currentBlock*packetPayloadLen:], packetPayloadLen)
				if err != nil {
					return err
				}
			} else {
				err := sendBlock(c, uint8((currentBlock+1)%256), data[currentBlock*packetPayloadLen:(currentBlock+1)*packetPayloadLen], packetPayloadLen)
				if err != nil {
					return err
				}
			}

			if _, err := c.Read(oBuffer); err != nil {
				return err
			}

			if oBuffer[0] == ACK {
				currentBlock++
				if currentBlock%100 == 0 {
					log.Debugf("Block %d/%d sent\n", currentBlock, blocks)
				}
			} else {
				failed++
				log.Debugf("Block %d failed. # of fails %d\n", currentBlock, failed)
			}
		}
		log.Debugf("Block %d/%d sent\n", currentBlock, blocks)
		log.Debugf("Failed %d times\n", failed)
		log.Debugf("Sending EOT...\n")
		if _, err := c.Write([]byte{EOT}); err != nil {
			return err
		}
	}

	return nil
}

func ModemReceive(c io.ReadWriter) ([]byte, error) {
	var data bytes.Buffer
	oBuffer := make([]byte, 1)
	dBuffer := make([]byte, XModemBlockLength)

	log.Println("Before")

	// Start Connection
	if _, err := c.Write([]byte{POLL}); err != nil {
		return nil, err
	}

	log.Println("Write Poll")

	// Read Packets
	for {
		if _, err := c.Read(oBuffer); err != nil {
			return nil, err
		}
		pType := oBuffer[0]
		log.Println("PType:", pType)

		if pType == EOT {
			if _, err := c.Write([]byte{ACK}); err != nil {
				return nil, err
			}
			break
		}

		var packetSize int
		switch pType {
		case SOH:
			log.Println("SOH: Use 128")
			packetSize = XModemBlockLength
		case STX:
			log.Println("STX: Use 1K")
			dBuffer = make([]byte, XModem1KBlockLength)
			packetSize = XModem1KBlockLength
		}

		if _, err := c.Read(oBuffer); err != nil {
			return nil, err
		}
		packetCount := oBuffer[0]

		if _, err := c.Read(oBuffer); err != nil {
			return nil, err
		}
		inverseCount := oBuffer[0]

		if packetCount > inverseCount || inverseCount+packetCount != 255 {
			if _, err := c.Write([]byte{NAK}); err != nil {
				return nil, err
			}
			continue
		}

		received := 0
		var pData bytes.Buffer
		for received < packetSize {
			n, err := c.Read(dBuffer)
			if err != nil {
				return nil, err
			}

			received += n
			pData.Write(dBuffer[:n])
		}

		var crc uint16
		if _, err := c.Read(oBuffer); err != nil {
			return nil, err
		}
		crc = uint16(oBuffer[0])

		if _, err := c.Read(oBuffer); err != nil {
			return nil, err
		}
		crc <<= 8
		crc |= uint16(oBuffer[0])

		// Calculate CRC
		crcCalc := CRC16(pData.Bytes())
		if crcCalc == crc {
			data.Write(pData.Bytes())
			if _, err := c.Write([]byte{ACK}); err != nil {
				return nil, err
			}
		} else {
			if _, err := c.Write([]byte{NAK}); err != nil {
				return nil, err
			}
		}
	}

	return data.Bytes(), nil
}
