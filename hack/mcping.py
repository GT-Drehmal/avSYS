#!/usr/bin/env python3
"""Speaks just enough of the Minecraft protocol to test the avSYS router.

    mcping.py <hostname> status|join [router-host] [router-port]

"status" prints the server list response for the hostname; "join" attempts a
login and prints what the router says back when it refuses one.
"""
import socket, struct, json, sys, base64, hashlib
def varint(n):
    out=b''; n &= 0xffffffff
    while True:
        b=n&0x7f; n>>=7
        if n: out+=bytes([b|0x80])
        else: return out+bytes([b])
def rv(s):
    n=0
    for i in range(5):
        b=s.recv(1)[0]; n|=(b&0x7f)<<(7*i)
        if not b&0x80: return n
def talk(host, state, addr):
    s=socket.create_connection(addr,timeout=5)
    h=host.encode()
    body=b'\x00'+varint(767)+varint(len(h))+h+struct.pack('>H',25565)+varint(state)
    pkt=varint(len(body))+body
    if state==1: pkt+=b'\x01\x00'
    else:
        name=b'tester'; lb=b'\x00'+varint(len(name))+name+bytes(16); pkt+=varint(len(lb))+lb
    s.sendall(pkt)
    ln=rv(s); pid=rv(s); sl=rv(s)
    data=b''
    while len(data)<sl: data+=s.recv(sl-len(data))
    d=json.loads(data)
    # The picture itself is long and says nothing here; which one it is does.
    if isinstance(d,dict) and 'favicon' in d:
        d['icon']=hashlib.sha1(base64.b64decode(d.pop('favicon').split(',',1)[1])).hexdigest()
    return d
addr=(sys.argv[3] if len(sys.argv)>3 else '127.0.0.1', int(sys.argv[4]) if len(sys.argv)>4 else 25565)
print(json.dumps(talk(sys.argv[1], 1 if sys.argv[2]=='status' else 2, addr)))
