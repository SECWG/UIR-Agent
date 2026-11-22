#!/bin/sh
# 固定的只读信息采集；不读取用户启动脚本，不安装依赖，不探测外部地址。
export LC_ALL=C
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
exec 2>/dev/null
command -v base64 >/dev/null || exit 1
emit() {
  printf '%s' "$1"; shift
  for field do printf '\t'; printf '%s' "$field" | base64 | tr -d '\r\n'; done
  printf '\n'
}
os=$(uname -s)
host=$(hostname)
kernel=$(uname -r)
arch=$(uname -m)
account=$(id -un)
uid=$(id -u)
privilege=
if [ "$uid" = 0 ]; then privilege=root; elif [ -n "$uid" ]; then privilege=user; fi
os_name=$os; os_version=; uptime=; cpu=; cores=; memory=; available=
if [ "$os" = Linux ]; then
  # os-release 是数据，不通过 source 或 eval 执行其中内容。
  if [ -r /etc/os-release ]; then
    os_name=$(awk -F= '$1=="PRETTY_NAME" {v=substr($0,index($0,"=")+1); gsub(/^"|"$/, "",v); print v; exit}' /etc/os-release)
    os_version=$(awk -F= '$1=="VERSION_ID" {v=substr($0,index($0,"=")+1); gsub(/^"|"$/, "",v); print v; exit}' /etc/os-release)
  fi
  uptime=$(awk '{printf "%.0f",int($1)}' /proc/uptime)
  cpu=$(awk -F: '/^(model name|Hardware|Processor)[[:space:]]*:/ {sub(/^[ \t]+/,"",$2); print $2; exit}' /proc/cpuinfo)
  cores=$(getconf _NPROCESSORS_ONLN)
  memory=$(awk '$1=="MemTotal:" {printf "%.0f",$2*1024}' /proc/meminfo)
  available=$(awk '$1=="MemAvailable:" {printf "%.0f",$2*1024}' /proc/meminfo)
elif [ "$os" = Darwin ]; then
  os_name=$(sw_vers -productName); os_version=$(sw_vers -productVersion)
  cpu=$(sysctl -n machdep.cpu.brand_string); [ -n "$cpu" ] || cpu=$(sysctl -n hw.model)
  cores=$(sysctl -n hw.logicalcpu); memory=$(sysctl -n hw.memsize)
  boot=$(sysctl -n kern.boottime | awk -F'[=,]' '{gsub(/ /,"",$2);print $2}')
  now=$(date +%s)
  case "$boot:$now" in *[!0-9:]*|:*|*:) ;; *) uptime=$((now-boot));; esac
  # macOS 没有等同 Linux MemAvailable 的原始计数，保留未知值。
fi
emit system "$host" "$os_name" "$os_version" "$kernel" "$arch" "$account" "$privilege" "$uptime" "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$(date +%Z)"
emit cpu "$cpu" "$cores"
emit memory "$memory" "$available"
if [ "$os" = Linux ]; then
  df -kP -T -l | awk 'NR>1 && $3 ~ /^[0-9]+$/ && $5 ~ /^[0-9]+$/ && $2 !~ /^(tmpfs|devtmpfs|squashfs|efivarfs)$/ {path=$7;for(i=8;i<=NF;i++)path=path " " $i;printf "%s\t%s\t%.0f\t%.0f\n",path,$2,$3*1024,$5*1024}' | head -n 64 |
    while IFS="$(printf '\t')" read -r mount fs total free; do emit disk "$mount" "$fs" "$total" "$free"; done
elif [ "$os" = Darwin ]; then
  df -kP -l | awk 'NR>1 && $2 ~ /^[0-9]+$/ && $4 ~ /^[0-9]+$/ && $1!="devfs" {path=$6;for(i=7;i<=NF;i++)path=path " " $i;printf "%s\t%s\t%.0f\t%.0f\n",path,"unknown",$2*1024,$4*1024}' | head -n 64 |
    while IFS="$(printf '\t')" read -r mount fs total free; do emit disk "$mount" "" "$total" "$free"; done
fi
if [ "$os" = Linux ] && [ -d /sys/class/net ]; then
  count=0
  for nic in /sys/class/net/*; do
    [ -d "$nic" ] || continue
    count=$((count+1)); [ "$count" -le 64 ] || break
    name=${nic##*/}; mac=$(cat "$nic/address"); state=$(cat "$nic/operstate")
    case "$state" in up|down) ;; *) state=unknown;; esac
    if command -v ip >/dev/null; then
      addresses=$(ip -o addr show dev "$name" | awk '$3=="inet" || $3=="inet6" {print $4}' | head -n 32)
    elif command -v ifconfig >/dev/null; then
      addresses=$(ifconfig "$name" | awk '$1=="inet" || $1=="inet6" {sub(/^addr:/,"",$2); print $2}' | head -n 32)
    else addresses=; fi
    emit network "$name" "$mac" "$state" "$addresses"
  done
elif command -v ifconfig >/dev/null; then
  count=0
  for name in $(ifconfig -l); do
    count=$((count+1)); [ "$count" -le 64 ] || break
    nic=$(ifconfig "$name"); mac=$(printf '%s\n' "$nic" | awk '$1=="ether" {print $2;exit}')
    state=$(printf '%s\n' "$nic" | awk '/status: active/{print "up";exit} /status: inactive/{print "down";exit}')
    [ -n "$state" ] || state=unknown
    addresses=$(printf '%s\n' "$nic" | awk '$1=="inet" || $1=="inet6" {print $2}' | head -n 32)
    emit network "$name" "$mac" "$state" "$addresses"
  done
fi
exit 0
