import sys
import json
import os

def main():
    if len(sys.argv) < 2:
        print(json.dumps({"status": "error", "msg": "No arguments provided"}))
        sys.exit(1)

    args = json.loads(sys.argv[1])
    skill_name = args.get("skill", "unknown")

    result = {
        "status": "success",
        "skill": skill_name,
        "msg": "Skill bridge is working",
        "cwd": os.getcwd(),
    }

    print(json.dumps(result, ensure_ascii=False))

if __name__ == "__main__":
    main()
