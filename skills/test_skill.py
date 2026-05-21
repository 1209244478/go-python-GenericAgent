import sys
import json
import os
import platform
import time

def main():
    if len(sys.argv) < 2:
        print(json.dumps({"status": "error", "msg": "No arguments provided"}))
        sys.exit(1)

    args = json.loads(sys.argv[1])

    action = args.get("action", "echo")

    if action == "echo":
        msg = args.get("message", "hello from python")
        result = {
            "status": "success",
            "action": action,
            "echo": msg,
            "python_version": platform.python_version(),
            "platform": platform.system(),
            "cwd": os.getcwd(),
            "pid": os.getpid(),
        }
        print(json.dumps(result, ensure_ascii=False))

    elif action == "compute":
        a = args.get("a", 0)
        b = args.get("b", 0)
        op = args.get("op", "add")
        if op == "add":
            value = a + b
        elif op == "mul":
            value = a * b
        else:
            value = None
        result = {
            "status": "success",
            "action": action,
            "a": a,
            "b": b,
            "op": op,
            "result": value,
        }
        print(json.dumps(result, ensure_ascii=False))

    elif action == "env":
        result = {
            "status": "success",
            "action": action,
            "env_pythonpath": os.environ.get("PYTHONPATH", ""),
            "env_path": os.environ.get("PATH", "")[:200],
            "cwd": os.getcwd(),
        }
        print(json.dumps(result, ensure_ascii=False))

    elif action == "error":
        print(json.dumps({"status": "error", "msg": "intentional error for testing"}))
        sys.exit(1)

    elif action == "slow":
        delay = args.get("delay", 2)
        time.sleep(delay)
        result = {
            "status": "success",
            "action": action,
            "slept": delay,
        }
        print(json.dumps(result, ensure_ascii=False))

    else:
        print(json.dumps({"status": "error", "msg": f"Unknown action: {action}"}))
        sys.exit(1)

if __name__ == "__main__":
    main()
