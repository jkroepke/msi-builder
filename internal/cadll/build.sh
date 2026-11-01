#!/bin/sh
# build.sh builds builderrun.dll for x86, x64 and arm64 from src/builderrun.c
# with clang and lld. The DLL is freestanding (no C runtime, no SDK), so the
# only build inputs are the source, the import definitions below and the
# compiler. /Brepro makes the output reproducible for one clang version;
# CI rebuilds the DLLs and compares them with the committed ones.
set -eu
cd "$(dirname "$0")"
CLANG=${CLANG:-clang}
DLLTOOL=${DLLTOOL:-llvm-dlltool}

# Only clang 18 reproduces the committed bytes; another major version
# builds a working DLL that fails the CI comparison, so refuse it. Apple
# clang numbers its releases differently and has no lld, so it is refused
# whatever its number.
want=18
have=$("$CLANG" --version | sed -n '/^Apple /d; s/.*clang version \([0-9]*\).*/\1/p' | head -n 1)
if [ "$have" != "$want" ]; then
  echo "build.sh: $CLANG is clang ${have:-?}, the committed DLLs need clang $want" >&2
  echo "build.sh: set CLANG=clang-$want DLLTOOL=llvm-dlltool-$want, or build in ubuntu:24.04 as ci.yaml does" >&2
  exit 1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# imports writes a .def for a system DLL; the second column is the stdcall
# argument size, which only the x86 import names carry.
imports() {
  echo "LIBRARY $1.dll"
  echo "EXPORTS"
  shift
  for f in "$@"; do
    if [ "$arch" = i386 ]; then
      echo "${f%%:*}@${f##*:}"
    else
      echo "${f%%:*}"
    fi
  done
}

for target in i686:i386:x86 x86_64:i386:x86-64:x64 aarch64:arm64:arm64; do
  triple=${target%%:*}
  rest=${target#*:}
  arch=${rest%:*}
  out=${rest##*:}
  mkdir -p "$tmp/$out"
  imports kernel32 CreateNamedPipeW:32 CreateFileW:28 CreateEventW:16 CreateProcessW:40 InitializeProcThreadAttributeList:16 \
    UpdateProcThreadAttribute:28 DeleteProcThreadAttributeList:4 CreateJobObjectW:8 \
    SetInformationJobObject:16 AssignProcessToJobObject:8 TerminateJobObject:8 ResumeThread:4 \
    ReadFile:20 GetOverlappedResult:16 CancelIoEx:8 WaitForSingleObject:8 WaitForMultipleObjects:16 \
    GetTickCount64:0 GetExitCodeProcess:8 CloseHandle:4 GetCurrentThread:0 GetCurrentProcess:0 \
    GetProcessHeap:0 HeapAlloc:12 HeapReAlloc:16 HeapFree:12 GetLastError:0 \
    MultiByteToWideChar:24 TerminateProcess:8 LocalFree:4 GetComputerNameW:8 \
    GetFileAttributesW:4 GetLongPathNameW:12 CompareStringOrdinal:20 \
    GetFinalPathNameByHandleW:16 ExpandEnvironmentStringsW:12 SetFileInformationByHandle:16 GetFileInformationByHandleEx:16 \
    CreateDirectoryW:8 RemoveDirectoryW:4 MoveFileExW:12 \
    CreateToolhelp32Snapshot:8 Process32FirstW:8 Process32NextW:8 OpenProcess:12 \
    QueryFullProcessImageNameW:16 GetCurrentProcessId:0 >"$tmp/$out/kernel32.def"
  imports advapi32 OpenThreadToken:16 DuplicateTokenEx:24 CreateProcessAsUserW:44 SystemFunction036:8 \
    ConvertStringSidToSidW:8 LookupAccountNameW:28 GetNamedSecurityInfoW:32 GetSecurityInfo:32 SetNamedSecurityInfoW:28 SetSecurityInfo:28 \
    GetSecurityDescriptorControl:12 InitializeAcl:12 GetAce:12 AddAce:20 AddAccessAllowedAceEx:20 \
    EqualSid:8 GetLengthSid:4 RegCreateKeyExW:36 RegOpenKeyExW:20 RegSetValueExW:24 \
    RegQueryValueExW:24 RegDeleteTreeW:8 RegCloseKey:4 RegEnumKeyExW:32 RegEnumValueW:32 OpenProcessToken:12 GetTokenInformation:20 ConvertSidToStringSidW:8 \
    ConvertStringSecurityDescriptorToSecurityDescriptorW:16 >"$tmp/$out/advapi32.def"
  imports msi MsiGetPropertyW:16 MsiCreateRecord:4 MsiRecordSetStringW:12 MsiRecordSetInteger:12 MsiProcessMessage:12 \
    MsiCloseHandle:4 MsiSetPropertyW:12 MsiFormatRecordW:16 MsiEvaluateConditionW:8 \
    MsiGetMode:8 MsiGetProductInfoExW:24 >"$tmp/$out/msi.def"
  imports authz AuthzInitializeResourceManager:24 AuthzInitializeContextFromSid:32 AuthzGetInformationFromContext:20 \
    AuthzFreeContext:4 AuthzFreeResourceManager:4 >"$tmp/$out/authz.def"
  imports ntdll NtCreateFile:44 NtSetInformationFile:20 RtlNtStatusToDosError:4 >"$tmp/$out/ntdll.def"
  imports user32 EnumWindows:8 GetWindowThreadProcessId:8 PostMessageW:16 GetClassNameW:12 \
    IsWindowVisible:4 GetWindow:8 >"$tmp/$out/user32.def"
  for lib in kernel32 advapi32 msi authz ntdll user32; do
    "$DLLTOOL" -m "$arch" -k -d "$tmp/$out/$lib.def" -l "$tmp/$out/$lib.lib"
  done
  "$CLANG" --target="$triple-pc-windows-msvc" -std=c11 -O2 -Wall -Wextra -Werror \
    -fno-builtin -ffreestanding -fno-stack-protector -mno-stack-arg-probe \
    -fno-exceptions \
    -c src/builderrun.c -o "$tmp/$out/builderrun.obj"
  "$CLANG" --target="$triple-pc-windows-msvc" -fuse-ld=lld -nostdlib -shared \
    -o "builderrun-$out.dll" "$tmp/$out/builderrun.obj" \
    "$tmp/$out/kernel32.lib" "$tmp/$out/advapi32.lib" "$tmp/$out/msi.lib" "$tmp/$out/authz.lib" "$tmp/$out/ntdll.lib" "$tmp/$out/user32.lib" \
    -Wl,/Brepro -Wl,/nodefaultlib -Wl,/entry:_DllMainCRTStartup -Wl,/export:BuilderRun -Wl,/export:BuilderGrant \
    -Wl,/export:BuilderClose -Wl,/export:BuilderCloseSchedule -Wl,/export:BuilderRemoveFoldersExec \
    -Wl,/release -Wl,/opt:ref -Wl,/opt:icf -Wl,/subsystem:windows
done
sha256sum builderrun-x86.dll builderrun-x64.dll builderrun-arm64.dll >SHA256SUMS
ls -l builderrun-*.dll
