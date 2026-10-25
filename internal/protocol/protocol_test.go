package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvelopeMarshal(t *testing.T) {
	env := Envelope{ID: "m_1", Type: TypeHeartbeat, TS: 1761139200,
		Payload: HeartbeatPayload{CPUPercent: 12.5}}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	// 信封字段齐全
	for _, key := range []string{`"id":"m_1"`, `"type":"heartbeat"`, `"ts":1761139200`, `"cpu_percent":12.5`} {
		if !strings.Contains(string(b), key) {
			t.Fatalf("信封缺少字段 %s: %s", key, b)
		}
	}
}

func TestHelloSigVerify(t *testing.T) {
	secret := "sec_abc"
	h := HelloPayload{NodeID: "n_1", TS: 1761139200, Nonce: "nonce1"}
	h.Sig = HelloSig(secret, h.NodeID, h.TS, h.Nonce)
	if !VerifyHelloSig(secret, h) {
		t.Fatal("正确凭据应通过")
	}
	if VerifyHelloSig("sec_other", h) {
		t.Fatal("错误凭据必须拒绝")
	}
	// 篡改任一字段
	tampered := h
	tampered.TS++
	if VerifyHelloSig(secret, tampered) {
		t.Fatal("篡改 ts 必须拒绝")
	}
}
