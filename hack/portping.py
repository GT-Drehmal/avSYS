#!/usr/bin/env python3
"""Reaches a server's extra ports as players' programs do, to test what
answers on them while the server's game is not running.

    portping.py bedrock <host> <port>   ping as Bedrock's server list does; prints the line it is answered with
    portping.py join <host> <port>      send what a Bedrock player joining sends first
    portping.py http <host> <port>      fetch / as a browser does; prints the answer, or "unreachable"
"""
import os, socket, struct, sys

MAGIC = bytes.fromhex('00ffff00fefefefefdfdfdfd12345678')

def bedrock(addr):
    ping = b'\x01' + struct.pack('>Q', 1234) + MAGIC + os.urandom(8)
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(1)
    # UDP: a ping or its answer can be lost on the way.
    for _ in range(5):
        s.sendto(ping, addr)
        try:
            b, _ = s.recvfrom(2048)
        except socket.timeout:
            continue
        if b[0] != 0x1c or b[1:9] != ping[1:9] or b[17:33] != MAGIC:
            sys.exit('not an answer to the ping: ' + b.hex())
        n = struct.unpack('>H', b[33:35])[0]
        return b[35:35 + n].decode()
    sys.exit('no answer')

def join(addr):
    # Open connection request 1: the id, the magic, RakNet's protocol, and
    # padding to the size of packet tried.
    request = b'\x05' + MAGIC + b'\x0b'
    request += bytes(1464 - len(request))
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    for _ in range(3):
        s.sendto(request, addr)
    return 'sent'

def http(addr):
    try:
        s = socket.create_connection(addr, timeout=5)
        s.sendall(b'GET / HTTP/1.1\r\nHost: map\r\nUser-Agent: avsys-e2e\r\nConnection: close\r\n\r\n')
        out = b''
        while True:
            b = s.recv(4096)
            if not b:
                break
            out += b
    except OSError:
        return 'unreachable'
    return out.decode(errors='replace') or 'unreachable'

what, host, port = sys.argv[1], sys.argv[2], int(sys.argv[3])
print({'bedrock': bedrock, 'join': join, 'http': http}[what]((host, port)))
