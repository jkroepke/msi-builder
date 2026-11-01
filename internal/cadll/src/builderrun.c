// builderrun.c is the custom action DLL that msi-builder embeds into packages for
// `run` actions. The entry point BuilderRun starts a command line with a
// hidden window, copies everything the process writes to stdout or stderr
// into the Windows Installer log through MsiProcessMessage, and turns the
// exit code into a custom action return value: 0 for exit code 0,
// ERROR_INSTALL_FAILURE for anything else.
//
// The DLL is freestanding: it includes no SDK header and links no C runtime,
// only kernel32.dll, advapi32.dll, msi.dll, user32.dll (for closing running
// programs) and ntdll.dll (for handle-relative opens and renames), whose
// prototypes are written out below from Microsoft's documentation of each
// function. It is built by
// build.sh with clang and lld for x86, x64 and arm64.
//
// Input, in CustomActionData for deferred, rollback and commit actions and in
// the property BuilderRun for immediate actions:
//
//     <working directory>|<command line>
//
// The directory may be empty; `|` cannot occur in a Windows path, so the
// first one ends it. A log line reads `BuilderRun: ...`.

typedef unsigned int UINT;
typedef unsigned long DWORD;
typedef int BOOL;
typedef void *HANDLE;
typedef unsigned short WCHAR;
typedef unsigned char BYTE;
typedef unsigned long MSIHANDLE;
typedef unsigned long long SIZE_T_;

#if defined(__i386__)
#define WINAPI __attribute__((stdcall))
typedef unsigned int SIZE_T;
typedef int INTPTR;
#else
#define WINAPI
typedef SIZE_T_ SIZE_T;
typedef long long INTPTR;
#endif

typedef struct {
    DWORD nLength;
    void *lpSecurityDescriptor;
    BOOL bInheritHandle;
} SECURITY_ATTRIBUTES;

typedef struct {
    DWORD cb;
    WCHAR *lpReserved;
    WCHAR *lpDesktop;
    WCHAR *lpTitle;
    DWORD dwX, dwY, dwXSize, dwYSize, dwXCountChars, dwYCountChars, dwFillAttribute, dwFlags;
    unsigned short wShowWindow, cbReserved2;
    BYTE *lpReserved2;
    HANDLE hStdInput, hStdOutput, hStdError;
} STARTUPINFOW;

typedef struct {
    HANDLE hProcess, hThread;
    DWORD dwProcessId, dwThreadId;
} PROCESS_INFORMATION;

// ACL, ACE_HEADER and ACCESS_ALLOWED_ACE as winnt.h documents them.
typedef struct {
    BYTE AclRevision;
    BYTE Sbz1;
    unsigned short AclSize;
    unsigned short AceCount;
    unsigned short Sbz2;
} ACL;

typedef struct {
    BYTE AceType;
    BYTE AceFlags;
    unsigned short AceSize;
} ACE_HEADER;

typedef struct {
    ACE_HEADER Header;
    DWORD Mask;
    DWORD SidStart;
} ACCESS_ALLOWED_ACE;

// OVERLAPPED, STARTUPINFOEXW and the job limit structures as the SDK
// headers lay them out; the 64-bit members keep the 8-byte alignment that
// gives JOBOBJECT_EXTENDED_LIMIT_INFORMATION its documented size (112 bytes
// on x86, 144 on x64 and arm64).
typedef struct {
    SIZE_T Internal, InternalHigh;
    DWORD Offset, OffsetHigh;
    HANDLE hEvent;
} OVERLAPPED;

typedef struct {
    STARTUPINFOW StartupInfo;
    void *lpAttributeList;
} STARTUPINFOEXW;

typedef struct {
    long long PerProcessUserTimeLimit, PerJobUserTimeLimit;
    DWORD LimitFlags;
    SIZE_T MinimumWorkingSetSize, MaximumWorkingSetSize;
    DWORD ActiveProcessLimit;
    SIZE_T Affinity;
    DWORD PriorityClass, SchedulingClass;
} JOBOBJECT_BASIC_LIMIT_INFORMATION;

typedef struct {
    JOBOBJECT_BASIC_LIMIT_INFORMATION BasicLimitInformation;
    unsigned long long IoInfo[6];
    SIZE_T ProcessMemoryLimit, JobMemoryLimit, PeakProcessMemoryUsed, PeakJobMemoryUsed;
} JOBOBJECT_EXTENDED_LIMIT_INFORMATION;


_Static_assert(sizeof(OVERLAPPED) == (sizeof(void *) == 4 ? 20 : 32), "OVERLAPPED layout");
_Static_assert(sizeof(STARTUPINFOEXW) == (sizeof(void *) == 4 ? 72 : 112), "STARTUPINFOEXW layout");
_Static_assert(sizeof(JOBOBJECT_EXTENDED_LIMIT_INFORMATION) == (sizeof(void *) == 4 ? 112 : 144), "JOBOBJECT_EXTENDED_LIMIT_INFORMATION layout");

// kernel32.dll
HANDLE WINAPI CreateNamedPipeW(const WCHAR *, DWORD, DWORD, DWORD, DWORD, DWORD, DWORD, SECURITY_ATTRIBUTES *);
HANDLE WINAPI CreateFileW(const WCHAR *, DWORD, DWORD, SECURITY_ATTRIBUTES *, DWORD, DWORD, HANDLE);
HANDLE WINAPI CreateEventW(SECURITY_ATTRIBUTES *, BOOL, BOOL, const WCHAR *);
BOOL WINAPI CreateProcessW(const WCHAR *, WCHAR *, SECURITY_ATTRIBUTES *, SECURITY_ATTRIBUTES *, BOOL, DWORD, void *, const WCHAR *, STARTUPINFOW *, PROCESS_INFORMATION *);
BOOL WINAPI InitializeProcThreadAttributeList(void *, DWORD, DWORD, SIZE_T *);
BOOL WINAPI UpdateProcThreadAttribute(void *, DWORD, SIZE_T, void *, SIZE_T, void *, SIZE_T *);
void WINAPI DeleteProcThreadAttributeList(void *);
HANDLE WINAPI CreateJobObjectW(SECURITY_ATTRIBUTES *, const WCHAR *);
BOOL WINAPI SetInformationJobObject(HANDLE, int, void *, DWORD);
BOOL WINAPI AssignProcessToJobObject(HANDLE, HANDLE);
BOOL WINAPI TerminateJobObject(HANDLE, UINT);
DWORD WINAPI ResumeThread(HANDLE);
BOOL WINAPI TerminateProcess(HANDLE, UINT);
BOOL WINAPI ReadFile(HANDLE, void *, DWORD, DWORD *, OVERLAPPED *);
BOOL WINAPI GetOverlappedResult(HANDLE, OVERLAPPED *, DWORD *, BOOL);
BOOL WINAPI CancelIoEx(HANDLE, OVERLAPPED *);
DWORD WINAPI WaitForSingleObject(HANDLE, DWORD);
DWORD WINAPI WaitForMultipleObjects(DWORD, const HANDLE *, BOOL, DWORD);
unsigned long long WINAPI GetTickCount64(void);
BOOL WINAPI GetExitCodeProcess(HANDLE, DWORD *);
BOOL WINAPI CloseHandle(HANDLE);
HANDLE WINAPI GetCurrentThread(void);
HANDLE WINAPI GetCurrentProcess(void);
HANDLE WINAPI GetProcessHeap(void);
void *WINAPI HeapAlloc(HANDLE, DWORD, SIZE_T);
void *WINAPI HeapReAlloc(HANDLE, DWORD, void *, SIZE_T);
BOOL WINAPI HeapFree(HANDLE, DWORD, void *);
DWORD WINAPI GetLastError(void);
int WINAPI MultiByteToWideChar(UINT, DWORD, const char *, int, WCHAR *, int);
void *WINAPI LocalFree(void *);
BOOL WINAPI GetComputerNameW(WCHAR *, DWORD *);
DWORD WINAPI GetFileAttributesW(const WCHAR *);
DWORD WINAPI GetLongPathNameW(const WCHAR *, WCHAR *, DWORD);
int WINAPI CompareStringOrdinal(const WCHAR *, int, const WCHAR *, int, BOOL);
DWORD WINAPI GetFinalPathNameByHandleW(HANDLE, WCHAR *, DWORD, DWORD);
DWORD WINAPI ExpandEnvironmentStringsW(const WCHAR *, WCHAR *, DWORD);
BOOL WINAPI SetFileInformationByHandle(HANDLE, int, void *, DWORD);
BOOL WINAPI GetFileInformationByHandleEx(HANDLE, int, void *, DWORD);
BOOL WINAPI CreateDirectoryW(const WCHAR *, SECURITY_ATTRIBUTES *);
BOOL WINAPI RemoveDirectoryW(const WCHAR *);
BOOL WINAPI MoveFileExW(const WCHAR *, const WCHAR *, DWORD);

// advapi32.dll
BOOL WINAPI OpenThreadToken(HANDLE, DWORD, BOOL, HANDLE *);
BOOL WINAPI OpenProcessToken(HANDLE, DWORD, HANDLE *);
BOOL WINAPI GetTokenInformation(HANDLE, int, void *, DWORD, DWORD *);
BOOL WINAPI ConvertSidToStringSidW(void *, WCHAR **);
BOOL WINAPI ConvertStringSecurityDescriptorToSecurityDescriptorW(const WCHAR *, DWORD, void **, DWORD *);
BOOL WINAPI DuplicateTokenEx(HANDLE, DWORD, SECURITY_ATTRIBUTES *, int, int, HANDLE *);
BOOL WINAPI CreateProcessAsUserW(HANDLE, const WCHAR *, WCHAR *, SECURITY_ATTRIBUTES *, SECURITY_ATTRIBUTES *, BOOL, DWORD, void *, const WCHAR *, STARTUPINFOW *, PROCESS_INFORMATION *);
// RtlGenRandom, exported by advapi32.dll under this name.
BYTE WINAPI SystemFunction036(void *, DWORD); // returns BOOLEAN, one byte

BOOL WINAPI ConvertStringSidToSidW(const WCHAR *, void **);
BOOL WINAPI LookupAccountNameW(const WCHAR *, const WCHAR *, void *, DWORD *, WCHAR *, DWORD *, int *);
DWORD WINAPI GetNamedSecurityInfoW(const WCHAR *, int, DWORD, void **, void **, ACL **, ACL **, void **);
DWORD WINAPI GetSecurityInfo(HANDLE, int, DWORD, void **, void **, ACL **, ACL **, void **);
DWORD WINAPI SetNamedSecurityInfoW(WCHAR *, int, DWORD, void *, void *, ACL *, ACL *);
DWORD WINAPI SetSecurityInfo(HANDLE, int, DWORD, void *, void *, ACL *, ACL *);
BOOL WINAPI GetSecurityDescriptorControl(void *, unsigned short *, DWORD *);
BOOL WINAPI InitializeAcl(ACL *, DWORD, DWORD);
BOOL WINAPI GetAce(ACL *, DWORD, void **);
BOOL WINAPI AddAce(ACL *, DWORD, DWORD, void *, DWORD);
BOOL WINAPI AddAccessAllowedAceEx(ACL *, DWORD, DWORD, DWORD, void *);
BOOL WINAPI EqualSid(void *, void *);
DWORD WINAPI GetLengthSid(void *);
long WINAPI RegCreateKeyExW(HANDLE, const WCHAR *, DWORD, WCHAR *, DWORD, DWORD, SECURITY_ATTRIBUTES *, HANDLE *, DWORD *);
long WINAPI RegOpenKeyExW(HANDLE, const WCHAR *, DWORD, DWORD, HANDLE *);
long WINAPI RegSetValueExW(HANDLE, const WCHAR *, DWORD, DWORD, const BYTE *, DWORD);
long WINAPI RegQueryValueExW(HANDLE, const WCHAR *, DWORD *, DWORD *, BYTE *, DWORD *);
long WINAPI RegDeleteTreeW(HANDLE, const WCHAR *);
long WINAPI RegCloseKey(HANDLE);
long WINAPI RegEnumKeyExW(HANDLE, DWORD, WCHAR *, DWORD *, DWORD *, WCHAR *, DWORD *, void *);
long WINAPI RegEnumValueW(HANDLE, DWORD, WCHAR *, DWORD *, DWORD *, DWORD *, BYTE *, DWORD *);

// msi.dll
UINT WINAPI MsiGetPropertyW(MSIHANDLE, const WCHAR *, WCHAR *, DWORD *);
MSIHANDLE WINAPI MsiCreateRecord(UINT);
UINT WINAPI MsiRecordSetStringW(MSIHANDLE, UINT, const WCHAR *);
UINT WINAPI MsiRecordSetInteger(MSIHANDLE, UINT, int);
int WINAPI MsiProcessMessage(MSIHANDLE, int, MSIHANDLE);
UINT WINAPI MsiCloseHandle(MSIHANDLE);
UINT WINAPI MsiSetPropertyW(MSIHANDLE, const WCHAR *, const WCHAR *);
UINT WINAPI MsiFormatRecordW(MSIHANDLE, MSIHANDLE, WCHAR *, DWORD *);
int WINAPI MsiEvaluateConditionW(MSIHANDLE, const WCHAR *);
BOOL WINAPI MsiGetMode(MSIHANDLE, int);
UINT WINAPI MsiGetProductInfoExW(const WCHAR *, const WCHAR *, int, const WCHAR *, WCHAR *, DWORD *);

// authz.dll: the groups of the account that started the session, from its
// SID, for the deferred side of the folder removal, which runs as
// LocalSystem.
typedef struct {
    DWORD LowPart;
    long HighPart;
} LUID_;
BOOL WINAPI AuthzInitializeResourceManager(DWORD, void *, void *, void *, const WCHAR *, void **);
BOOL WINAPI AuthzInitializeContextFromSid(DWORD, void *, void *, void *, LUID_, void *, void **);
BOOL WINAPI AuthzGetInformationFromContext(void *, int, DWORD, DWORD *, void *);
BOOL WINAPI AuthzFreeContext(void *);
BOOL WINAPI AuthzFreeResourceManager(void *);

// ntdll.dll: NtCreateFile opens a name relative to a directory handle,
// which no kernel32 function does, and NtSetInformationFile renames into
// a directory handle, which SetFileInformationByHandle refuses with
// ERROR_INVALID_PARAMETER (FILE-11, CI run 41064763472).
typedef struct {
    unsigned short Length, MaximumLength;
    WCHAR *Buffer;
} UNICODE_STRING;
typedef struct {
    DWORD Length;
    HANDLE RootDirectory;
    UNICODE_STRING *ObjectName;
    DWORD Attributes;
    void *SecurityDescriptor, *SecurityQualityOfService;
} OBJECT_ATTRIBUTES;
typedef struct {
    union {
        long Status;
        void *Pointer;
    } u;
    SIZE_T Information;
} IO_STATUS_BLOCK;
_Static_assert(sizeof(OBJECT_ATTRIBUTES) == (sizeof(void *) == 4 ? 24 : 48), "OBJECT_ATTRIBUTES layout");
_Static_assert(sizeof(IO_STATUS_BLOCK) == (sizeof(void *) == 4 ? 8 : 16), "IO_STATUS_BLOCK layout");
long WINAPI NtCreateFile(HANDLE *, DWORD, OBJECT_ATTRIBUTES *, IO_STATUS_BLOCK *, void *, DWORD, DWORD, DWORD, DWORD, void *, DWORD);
long WINAPI NtSetInformationFile(HANDLE, IO_STATUS_BLOCK *, void *, DWORD, int);
DWORD WINAPI RtlNtStatusToDosError(long);

// Values that do not fit an int are macros: C11 allows only int-sized
// enumerators (6.7.2.2), and clang accepts larger ones silently.
#define PROTECTED_DACL_SECURITY_INFORMATION 0x80000000u
#define MAXDWORD_ 0xFFFFFFFFu
#define INVALID_HANDLE_VALUE ((HANDLE)(INTPTR)-1)
#define INVALID_FILE_ATTRIBUTES 0xFFFFFFFFu

enum {
    ERROR_SUCCESS = 0,
    ERROR_IO_PENDING = 997,
    ERROR_BROKEN_PIPE = 109,
    ERROR_MORE_DATA = 234,
    ERROR_NO_MORE_ITEMS = 259,
    ERROR_NO_TOKEN = 1008,
    ERROR_INSTALL_USEREXIT = 1602,
    ERROR_INSTALL_FAILURE = 1603,
    ERROR_INSUFFICIENT_BUFFER = 122,

    STARTF_USESTDHANDLES = 0x100,
    CREATE_SUSPENDED = 4,
    CREATE_NO_WINDOW = 0x08000000,
    EXTENDED_STARTUPINFO_PRESENT = 0x00080000,
    PROC_THREAD_ATTRIBUTE_HANDLE_LIST = 0x20002,
    WAIT_OBJECT_0 = 0,
    WAIT_TIMEOUT = 258,

    PIPE_ACCESS_INBOUND = 1,
    FILE_FLAG_OVERLAPPED = 0x40000000,
    FILE_FLAG_FIRST_PIPE_INSTANCE = 0x00080000,
    PIPE_REJECT_REMOTE_CLIENTS = 8,
    GENERIC_WRITE = 0x40000000,
    OPEN_EXISTING = 3,

    JobObjectExtendedLimitInformation = 9,
    JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x2000,

    TOKEN_DUPLICATE = 2,
    TOKEN_QUERY = 8,
    MAXIMUM_ALLOWED = 0x02000000,
    SecurityImpersonation = 2,
    TokenPrimary = 1,

    HEAP_ZERO_MEMORY = 8,
    CP_OEMCP = 1,
    CP_UTF8 = 65001,
    MB_ERR_INVALID_CHARS = 8,

    INSTALLMESSAGE_ERROR = 0x01000000,
    INSTALLMESSAGE_INFO = 0x04000000,
    INSTALLMESSAGE_PROGRESS = 0x0A000000,
    IDCANCEL = 2,

    SE_FILE_OBJECT = 1,
    SE_SERVICE = 2,
    DACL_SECURITY_INFORMATION = 4,
    UNPROTECTED_DACL_SECURITY_INFORMATION = 0x20000000,
    SE_DACL_PROTECTED = 0x1000,
    OWNER_SECURITY_INFORMATION = 1,
    READ_CONTROL_ = 0x20000,
    WRITE_DAC_ = 0x40000,
    FileStandardInfo_ = 1,
    ACL_REVISION = 2,
    ACCESS_ALLOWED_ACE_TYPE = 0,
    INHERITED_ACE = 0x10,
    VALID_INHERIT_FLAGS = 0x1F,
    // The inheritance flags a grant may author: INHERITED_ACE is the
    // system's mark, and an ACE that claims it is dropped at the next
    // propagation.
    AUTHORED_INHERIT_FLAGS = VALID_INHERIT_FLAGS & ~INHERITED_ACE,

    KEY_QUERY_VALUE = 0x1,
    KEY_WOW64_64KEY = 0x100,
    KEY_ENUMERATE_SUB_KEYS = 0x8,
    REG_EXPAND_SZ = 2,
    FILE_READ_ATTRIBUTES = 0x80,
    FILE_SHARE_ALL = 7,
    FILE_FLAG_BACKUP_SEMANTICS = 0x02000000,
    KEY_SET_VALUE = 0x2,
    REG_SZ = 1,
    REG_BINARY = 3,
    REG_DWORD = 4,
    MSICONDITION_TRUE = 1,
    MSIRUNMODE_ROLLBACKENABLED = 3,
    FILE_FLAG_OPEN_REPARSE_POINT = 0x00200000,
    DELETE_ = 0x10000,
    SYNCHRONIZE_ = 0x100000,
    FILE_LIST_DIRECTORY = 1,
    FILE_ADD_SUBDIRECTORY = 4,
    FILE_WRITE_ATTRIBUTES = 0x100,
    FILE_ATTRIBUTE_READONLY = 1,
    FILE_ATTRIBUTE_NORMAL = 0x80,
    ERROR_ALREADY_EXISTS = 183,
    ERROR_NO_MORE_FILES = 18,
    ERROR_NOT_ENOUGH_MEMORY = 8,
    ERROR_NOT_SAME_DEVICE = 17,
    // FILE_INFO_BY_HANDLE_CLASS values.
    FileBasicInfo_ = 0,
    FileDispositionInfo_ = 4,
    FileDispositionInfoEx_ = 21,
    FileAttributeTagInfo_ = 9,
    FileIdInfo_ = 18,
    FileFullDirectoryInfo_ = 14,
    FileFullDirectoryRestartInfo_ = 15,
    // Authz.
    AUTHZ_RM_FLAG_NO_AUDIT = 1,
    AuthzContextInfoGroupsSids = 2,
    // NtSetInformationFile FILE_INFORMATION_CLASS.
    FileRenameInformation_ = 10,
    // NtCreateFile.
    OBJ_CASE_INSENSITIVE = 0x40,
    FILE_OPEN_ = 1,
    FILE_SYNCHRONOUS_IO_NONALERT = 0x20,
    FILE_OPEN_FOR_BACKUP_INTENT = 0x4000,
    FILE_OPEN_REPARSE_POINT = 0x00200000,
    FILE_ATTRIBUTE_DIRECTORY = 0x10,
    FILE_ATTRIBUTE_REPARSE_POINT = 0x400,
    CSTR_EQUAL = 2,
    MSICONDITION_NONE = 2,

    // SID_NAME_USE
    SidTypeDomain = 3,
    SidTypeDeletedAccount = 6,
    SidTypeInvalid = 7,
    SidTypeUnknown = 8,

    // Output handling: the line buffer, which is also the longest line
    // logged in one piece; how long the pipe is read after the child
    // exited while another process keeps it open; and how often a silent
    // child is interrupted to ask the installer about a cancel.
    LINE_BUFFER = 65536,
    EXIT_GRACE_MS = 5000,
    // How much output is still read after the grace period: more than the
    // pipe's buffer (LINE_BUFFER) can hold, so that only a process that
    // keeps writing reaches it.
    LATE_OUTPUT = 4 * LINE_BUFFER,
    CANCEL_POLL_MS = 1000,
};

// The compiler may lower struct copies and zeroing to these.
void *memset(void *dst, int value, SIZE_T len)
{
    BYTE *p = dst;
    while (len--) {
        *p++ = (BYTE)value;
    }
    return dst;
}

void *memcpy(void *dst, const void *src, SIZE_T len)
{
    BYTE *d = dst;
    const BYTE *s = src;
    while (len--) {
        *d++ = *s++;
    }
    return dst;
}

// moveDown copies len bytes to a lower address of the same buffer, front to
// back, which memcpy does not promise for overlapping ranges.
static void moveDown(void *dst, const void *src, SIZE_T len)
{
    BYTE *d = dst;
    const BYTE *s = src;
    while (len--) {
        *d++ = *s++;
    }
}

static SIZE_T wlen(const WCHAR *s)
{
    SIZE_T n = 0;
    while (s[n]) {
        n++;
    }
    return n;
}

static void wappend(WCHAR *dst, SIZE_T *pos, const WCHAR *src)
{
    while (*src) {
        dst[(*pos)++] = *src++;
    }
    dst[*pos] = 0;
}

static void wappendnum(WCHAR *dst, SIZE_T *pos, DWORD value)
{
    WCHAR digits[12];
    int n = 0;
    do {
        digits[n++] = (WCHAR)('0' + value % 10);
        value /= 10;
    } while (value);
    while (n) {
        dst[(*pos)++] = digits[--n];
    }
    dst[*pos] = 0;
}

static void wappendhex(WCHAR *dst, SIZE_T *pos, BYTE value)
{
    static const char digits[] = "0123456789abcdef";
    dst[(*pos)++] = (WCHAR)digits[value >> 4];
    dst[(*pos)++] = (WCHAR)digits[value & 15];
    dst[*pos] = 0;
}

// Log templates: the text goes into field 1 and the template is fixed, so
// brackets in process output or object names are not formatted as
// properties.
static const WCHAR tmplRun[] = {'B', 'u', 'i', 'l', 'd', 'e', 'r', 'R', 'u', 'n', ':', ' ', '[', '1', ']', 0};
static const WCHAR tmplGrant[] = {'B', 'u', 'i', 'l', 'd', 'e', 'r', 'G', 'r', 'a', 'n', 't', ':', ' ', '[', '1', ']', 0};

// logt writes one informational line to the installer log.
static int logt(MSIHANDLE install, const WCHAR *template, const WCHAR *text)
{
    MSIHANDLE rec = MsiCreateRecord(1);
    if (!rec) {
        return 0;
    }
    MsiRecordSetStringW(rec, 0, template);
    MsiRecordSetStringW(rec, 1, text);
    int ret = MsiProcessMessage(install, INSTALLMESSAGE_INFO, rec);
    MsiCloseHandle(rec);
    return ret;
}

static int logw(MSIHANDLE install, const WCHAR *text)
{
    return logt(install, tmplRun, text);
}

// logft writes "<what>: <detail> (error <code>)" without the code when it is 0.
static void logft(MSIHANDLE install, const WCHAR *template, const WCHAR *what, const WCHAR *detail, DWORD code)
{
    static const WCHAR sep[] = {':', ' ', 0};
    static const WCHAR errOpen[] = {' ', '(', 'e', 'r', 'r', 'o', 'r', ' ', 0};
    static const WCHAR errClose[] = {')', 0};
    SIZE_T n = wlen(what) + 2 + (detail ? wlen(detail) : 0) + 24;
    WCHAR *buf = HeapAlloc(GetProcessHeap(), 0, (n + 1) * sizeof(WCHAR));
    if (!buf) {
        return;
    }
    SIZE_T pos = 0;
    wappend(buf, &pos, what);
    if (detail) {
        wappend(buf, &pos, sep);
        wappend(buf, &pos, detail);
    }
    if (code) {
        wappend(buf, &pos, errOpen);
        wappendnum(buf, &pos, code);
        wappend(buf, &pos, errClose);
    }
    logt(install, template, buf);
    HeapFree(GetProcessHeap(), 0, buf);
}

static void logf(MSIHANDLE install, const WCHAR *what, const WCHAR *detail, DWORD code)
{
    logft(install, tmplRun, what, detail, code);
}

// cancelled asks the installer whether the user cancelled: a progress
// message that adds nothing to the bar is not logged and returns IDCANCEL
// after a cancel, like any message from a custom action.
static int cancelled(MSIHANDLE install)
{
    MSIHANDLE rec = MsiCreateRecord(3);
    if (!rec) {
        return 0;
    }
    MsiRecordSetInteger(rec, 1, 2);
    MsiRecordSetInteger(rec, 2, 0);
    MsiRecordSetInteger(rec, 3, 0);
    int ret = MsiProcessMessage(install, INSTALLMESSAGE_PROGRESS, rec);
    MsiCloseHandle(rec);
    return ret == IDCANCEL;
}

// property reads a property into a heap buffer, or returns 0 when it is
// empty or unreadable.
static WCHAR *property(MSIHANDLE install, const WCHAR *name)
{
    WCHAR probe[1];
    DWORD size = 0;
    UINT err = MsiGetPropertyW(install, name, probe, &size);
    if (err != ERROR_MORE_DATA && err != ERROR_SUCCESS) {
        return 0;
    }
    if (size == 0) {
        return 0;
    }
    size++;
    WCHAR *buf = HeapAlloc(GetProcessHeap(), HEAP_ZERO_MEMORY, size * sizeof(WCHAR));
    if (!buf) {
        return 0;
    }
    if (MsiGetPropertyW(install, name, buf, &size) != ERROR_SUCCESS || size == 0) {
        HeapFree(GetProcessHeap(), 0, buf);
        return 0;
    }
    return buf;
}

// emitLine converts one line of process output to UTF-16 and logs it. UTF-8
// is tried first, so Go programs and most modern tools log correctly; invalid
// UTF-8 is read in the OEM code page, which is what cmd.exe and PowerShell
// write to a redirected pipe.
static int emitLine(MSIHANDLE install, const char *line, int len)
{
    if (len == 0) {
        static const WCHAR empty[] = {0};
        return logw(install, empty);
    }
    int n = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, line, len, 0, 0);
    UINT cp = CP_UTF8;
    if (n == 0) {
        cp = CP_OEMCP;
        n = MultiByteToWideChar(CP_OEMCP, 0, line, len, 0, 0);
    }
    if (n == 0) {
        return 0;
    }
    WCHAR *buf = HeapAlloc(GetProcessHeap(), 0, ((SIZE_T)n + 1) * sizeof(WCHAR));
    if (!buf) {
        return 0;
    }
    n = MultiByteToWideChar(cp, 0, line, len, buf, n);
    buf[n] = 0;
    int ret = logw(install, buf);
    HeapFree(GetProcessHeap(), 0, buf);
    return ret;
}

static const WCHAR msgNoData[] = {'n', 'o', ' ', 'c', 'o', 'm', 'm', 'a', 'n', 'd', ' ', 'd', 'a', 't', 'a', 0};
static const WCHAR msgBadData[] = {'m', 'a', 'l', 'f', 'o', 'r', 'm', 'e', 'd', ' ', 'c', 'o', 'm', 'm', 'a', 'n', 'd', ' ', 'd', 'a', 't', 'a', 0};
static const WCHAR msgPipe[] = u"cannot create the output pipe";
static const WCHAR msgEvent[] = u"CreateEvent failed";
static const WCHAR msgAttributes[] = u"cannot build the handle list";
static const WCHAR msgJob[] = u"CreateJobObject failed; a process the command leaves behind is not ended";
static const WCHAR msgAssign[] = u"AssignProcessToJobObject failed; a process the command leaves behind is not ended";
static const WCHAR msgMemory[] = u"out of memory";
static const WCHAR msgStart[] = {'s', 't', 'a', 'r', 't', 'i', 'n', 'g', ' ', 'i', 'n', 0};
static const WCHAR msgDefaultDir[] = {'(', 'c', 'u', 'r', 'r', 'e', 'n', 't', ' ', 'd', 'i', 'r', 'e', 'c', 't', 'o', 'r', 'y', ')', 0};
static const WCHAR msgToken[] = {'t', 'o', 'k', 'e', 'n', 0};
static const WCHAR msgAsUser[] = {'t', 'h', 'e', ' ', 'i', 'm', 'p', 'e', 'r', 's', 'o', 'n', 'a', 't', 'e', 'd', ' ', 'u', 's', 'e', 'r', 0};
static const WCHAR msgAsProcess[] = {'t', 'h', 'e', ' ', 'p', 'r', 'o', 'c', 'e', 's', 's', 0};
static const WCHAR msgOpenToken[] = u"OpenThreadToken failed; not starting the command with the process token instead";
static const WCHAR msgCreate[] = {'C', 'r', 'e', 'a', 't', 'e', 'P', 'r', 'o', 'c', 'e', 's', 's', ' ', 'f', 'a', 'i', 'l', 'e', 'd', 0};
static const WCHAR msgDupToken[] = {'D', 'u', 'p', 'l', 'i', 'c', 'a', 't', 'e', 'T', 'o', 'k', 'e', 'n', 'E', 'x', ' ', 'f', 'a', 'i', 'l', 'e', 'd', 0};
static const WCHAR msgExit[] = {'e', 'x', 'i', 't', ' ', 'c', 'o', 'd', 'e', 0};
static const WCHAR msgCancel[] = {'c', 'a', 'n', 'c', 'e', 'l', 'l', 'e', 'd', ' ', 'b', 'y', ' ', 't', 'h', 'e', ' ', 'u', 's', 'e', 'r', 0};
static const WCHAR msgAbandon[] = u"a process the command started keeps the output open after the command exited; its further output is not logged and it is ended";
static const WCHAR msgReadFail[] = u"reading the output failed; further output is not logged";
static const WCHAR msgResume[] = u"ResumeThread failed";
static const WCHAR propData[] = {'C', 'u', 's', 't', 'o', 'm', 'A', 'c', 't', 'i', 'o', 'n', 'D', 'a', 't', 'a', 0};
static const WCHAR propRun[] = {'B', 'u', 'i', 'l', 'd', 'e', 'r', 'R', 'u', 'n', 0};

// output is the state of the child's output: the pipe, its read event, the
// job that holds the child and what it starts, and the line buffer.
typedef struct {
    HANDLE pipe, event, process, job;
    char *buf;
    SIZE_T len;
    int skipLF; // the last byte was \r: a following \n ends no line
    int cancelled;
} output;

// stop ends the command: the job takes every process it started with it,
// the process alone when there is no job.
static void stop(output *o)
{
    if (o->job) {
        TerminateJobObject(o->job, 1);
    } else {
        TerminateProcess(o->process, 1);
    }
}

// pieceEnd returns where a piece of a line too long for the buffer ends:
// before a UTF-8 sequence that the last bytes of buf begin but do not
// complete, so that each half is not decoded on its own, which fails UTF-8
// and logs both in the OEM code page. The bytes after it stay for the next
// piece. Text that is no UTF-8 loses nothing by it: at most three bytes
// move to the next piece.
static SIZE_T pieceEnd(const char *buf, SIZE_T len)
{
    SIZE_T lead = len;
    while (lead > 0 && len - lead < 3 && ((unsigned char)buf[lead - 1] & 0xC0) == 0x80) {
        lead--;
    }
    if (lead == 0) {
        return len;
    }
    unsigned char c = (unsigned char)buf[lead - 1];
    SIZE_T need = c >= 0xF0 && c <= 0xF4 ? 4 : c >= 0xE0 && c < 0xF0 ? 3 : c >= 0xC2 && c < 0xE0 ? 2 : 0;
    return need && len - (lead - 1) < need ? lead - 1 : len;
}

// emitLines logs the complete lines in the buffer, which the first n bytes
// of the last read extended, and keeps the rest. A line ends at \n, \r or
// \r\n, so progress written with \r alone is logged line by line; a line
// longer than the buffer is logged in pieces.
static void emitLines(MSIHANDLE install, output *o, SIZE_T scanFrom)
{
    SIZE_T start = 0;
    for (SIZE_T i = scanFrom; i < o->len; i++) {
        char c = o->buf[i];
        if (c == '\n' && o->skipLF && i == start) {
            o->skipLF = 0;
            start = i + 1;
            continue;
        }
        o->skipLF = 0;
        if (c != '\n' && c != '\r') {
            continue;
        }
        if (emitLine(install, o->buf + start, (int)(i - start)) == IDCANCEL && !o->cancelled) {
            o->cancelled = 1;
            stop(o);
        }
        o->skipLF = c == '\r';
        start = i + 1;
    }
    if (start) {
        moveDown(o->buf, o->buf + start, o->len - start);
        o->len -= start;
    }
    if (o->len == LINE_BUFFER) {
        SIZE_T cut = pieceEnd(o->buf, LINE_BUFFER);
        if (emitLine(install, o->buf, (int)cut) == IDCANCEL && !o->cancelled) {
            o->cancelled = 1;
            stop(o);
        }
        moveDown(o->buf, o->buf + cut, LINE_BUFFER - cut);
        o->len = LINE_BUFFER - cut;
    }
}

// abandon stops reading the pipe while the command may still run: the
// pipe is closed, so the child's writes fail instead of blocking on a full
// pipe, and the caller waits for the child without it.
static void abandon(output *o)
{
    CloseHandle(o->pipe);
    o->pipe = 0;
}

// pump reads the pipe until every writer has closed it and logs the lines.
// The read is overlapped, so the wait also watches the child and the
// installer: when the child has exited and another process keeps the pipe
// open past EXIT_GRACE_MS, that process is ended and the rest is not read;
// while the child is silent, the installer is asked every CANCEL_POLL_MS
// whether the user cancelled. A cancel, also one returned for a logged
// line, ends the job; the pipe then closes by itself.
//
// The grace period limits the wait for more output, not the reading of
// output that is already in the pipe: logging a long backlog can take
// longer than the grace period after the writers have closed the pipe, and
// that output is still read to its end. A writer that keeps the pipe filled
// is caught by the amount: once the grace period is over, the pipe holds
// at most its buffer when nobody writes any more, so more than
// LATE_OUTPUT bytes read after it means a process still writes.
static void pump(MSIHANDLE install, output *o)
{
    int exited = 0;
    unsigned long long deadline = 0, lastPoll = GetTickCount64();
    SIZE_T late = 0;
    for (;;) {
        if (!exited && WaitForSingleObject(o->process, 0) == WAIT_OBJECT_0) {
            exited = 1;
            deadline = GetTickCount64() + EXIT_GRACE_MS;
        }
        int over = exited && GetTickCount64() >= deadline;
        if (over && late > LATE_OUTPUT) {
            logf(install, msgAbandon, 0, 0);
            stop(o);
            abandon(o);
            break;
        }
        OVERLAPPED ov;
        memset(&ov, 0, sizeof ov);
        ov.hEvent = o->event;
        DWORD got = 0;
        SIZE_T scanFrom = o->len;
        int eof = 0;
        if (!ReadFile(o->pipe, o->buf + o->len, (DWORD)(LINE_BUFFER - o->len), &got, &ov)) {
            DWORD err = GetLastError();
            if (err == ERROR_BROKEN_PIPE) {
                break;
            }
            if (err != ERROR_IO_PENDING) {
                logf(install, msgReadFail, 0, err);
                abandon(o);
                break;
            }
            int done = 0;
            while (!done) {
                HANDLE handles[2] = {o->event, o->process};
                // After the child exited, the wait for more output ends
                // with the grace period.
                DWORD slice = CANCEL_POLL_MS;
                if (exited) {
                    unsigned long long now = GetTickCount64();
                    slice = now >= deadline ? 0 : deadline - now < CANCEL_POLL_MS ? (DWORD)(deadline - now) : CANCEL_POLL_MS;
                }
                DWORD r = WaitForMultipleObjects(exited ? 1 : 2, handles, 0, slice);
                if (r == WAIT_OBJECT_0) {
                    if (!GetOverlappedResult(o->pipe, &ov, &got, 0)) {
                        err = GetLastError();
                        if (err != ERROR_BROKEN_PIPE) {
                            logf(install, msgReadFail, 0, err);
                            abandon(o);
                        }
                        eof = 1;
                    }
                    done = 1;
                } else if (r == WAIT_OBJECT_0 + 1) {
                    exited = 1;
                    deadline = GetTickCount64() + EXIT_GRACE_MS;
                } else if (r != WAIT_TIMEOUT || (exited && GetTickCount64() >= deadline)) {
                    if (r == WAIT_TIMEOUT) {
                        logf(install, msgAbandon, 0, 0);
                    } else {
                        logf(install, msgReadFail, 0, GetLastError());
                    }
                    stop(o);
                    CancelIoEx(o->pipe, &ov);
                    GetOverlappedResult(o->pipe, &ov, &got, 1);
                    eof = 1;
                    done = 1;
                } else if (!o->cancelled) {
                    lastPoll = GetTickCount64();
                    if (cancelled(install)) {
                        o->cancelled = 1;
                        stop(o);
                    }
                }
            }
        }
        if (eof) {
            break;
        }
        // A zero-length write by the child reads as zero bytes; only a broken
        // pipe is the end.
        o->len += got;
        // Every read counts at least one byte: a writer that keeps
        // writing zero bytes never fills the pipe and would never end the
        // loop otherwise.
        if (exited && GetTickCount64() >= deadline) {
            late += got + 1;
        }
        emitLines(install, o, scanFrom);
        // A child that writes without line ends completes no line and times
        // out no wait, so the poll runs on the clock here as well, also
        // while a backlog is read after the child exited.
        if (!o->cancelled && GetTickCount64() - lastPoll >= CANCEL_POLL_MS) {
            lastPoll = GetTickCount64();
            if (cancelled(install)) {
                o->cancelled = 1;
                stop(o);
            }
        }
    }
    if (o->len && emitLine(install, o->buf, (int)o->len) == IDCANCEL && !o->cancelled) {
        o->cancelled = 1;
        stop(o);
    }
}

// await waits for the command to exit, asking the installer about a cancel
// while it runs.
static void await(MSIHANDLE install, output *o)
{
    while (WaitForSingleObject(o->process, CANCEL_POLL_MS) == WAIT_TIMEOUT) {
        if (!o->cancelled && cancelled(install)) {
            o->cancelled = 1;
            stop(o);
        }
    }
}

// parse splits "<dir>|<command>" into dir (0 when empty) and command. `|`
// cannot occur in a Windows path, so the first one ends the directory, which
// is terminated in place.
static int parse(WCHAR *data, WCHAR **dir, WCHAR **cmd)
{
    SIZE_T i = 0;
    while (data[i] && data[i] != '|') {
        i++;
    }
    if (data[i] != '|') {
        return 0;
    }
    *dir = i ? data : 0;
    // The command needs a buffer of its own: CreateProcessW may modify it.
    SIZE_T cmdLen = wlen(data + i + 1);
    WCHAR *copy = HeapAlloc(GetProcessHeap(), 0, (cmdLen + 1) * sizeof(WCHAR));
    if (!copy) {
        return 0;
    }
    memcpy(copy, data + i + 1, (cmdLen + 1) * sizeof(WCHAR));
    *cmd = copy;
    data[i] = 0;
    return 1;
}

// makePipe creates the pipe the child writes to: a named pipe with a random
// name, because only a named pipe can be read overlapped. The server end is
// ours and not inheritable; the client end is the only inheritable handle
// the child gets. The random name, the single instance and the default
// security of a named pipe (full control for the creator, LocalSystem and
// administrators, read for everyone else) keep any other account from
// writing into the log.
static int makePipe(MSIHANDLE install, HANDLE *server, HANDLE *client)
{
    static const WCHAR prefix[] = u"\\\\.\\pipe\\BuilderRun-";
    BYTE random[16];
    if (!SystemFunction036(random, sizeof random)) {
        logf(install, msgPipe, 0, GetLastError());
        return 0;
    }
    WCHAR name[64];
    SIZE_T pos = 0;
    name[0] = 0;
    wappend(name, &pos, prefix);
    for (SIZE_T i = 0; i < sizeof random; i++) {
        wappendhex(name, &pos, random[i]);
    }
    SECURITY_ATTRIBUTES own = {sizeof own, 0, 0}, inherit = {sizeof inherit, 0, 1};
    *server = CreateNamedPipeW(name, PIPE_ACCESS_INBOUND | FILE_FLAG_OVERLAPPED | FILE_FLAG_FIRST_PIPE_INSTANCE, PIPE_REJECT_REMOTE_CLIENTS, 1,
                               LINE_BUFFER, LINE_BUFFER, 0, &own);
    if (*server == INVALID_HANDLE_VALUE) {
        logf(install, msgPipe, 0, GetLastError());
        *server = 0;
        return 0;
    }
    *client = CreateFileW(name, GENERIC_WRITE, 0, &inherit, OPEN_EXISTING, 0, 0);
    if (*client == INVALID_HANDLE_VALUE) {
        logf(install, msgPipe, 0, GetLastError());
        CloseHandle(*server);
        *server = 0;
        *client = 0;
        return 0;
    }
    return 1;
}

// start creates the child, suspended, with the impersonated user's token
// when the thread impersonates one (an impersonated deferred action) and
// with the process token when it has none. CreateProcessW alone would
// ignore the impersonation token. Any other failure to read the thread's
// token fails the action rather than start the command as the process.
static BOOL start(MSIHANDLE install, WCHAR *cmd, const WCHAR *dir, STARTUPINFOEXW *si, PROCESS_INFORMATION *pi)
{
    const DWORD flags = CREATE_NO_WINDOW | CREATE_SUSPENDED | EXTENDED_STARTUPINFO_PRESENT;
    HANDLE thread = 0, primary = 0;
    BOOL ok;
    if (OpenThreadToken(GetCurrentThread(), TOKEN_QUERY | TOKEN_DUPLICATE, 1, &thread)) {
        if (!DuplicateTokenEx(thread, MAXIMUM_ALLOWED, 0, SecurityImpersonation, TokenPrimary, &primary)) {
            logf(install, msgDupToken, 0, GetLastError());
            CloseHandle(thread);
            return 0;
        }
        logf(install, msgToken, msgAsUser, 0);
        ok = CreateProcessAsUserW(primary, 0, cmd, 0, 0, 1, flags, 0, dir, &si->StartupInfo, pi);
        DWORD err = GetLastError();
        CloseHandle(primary);
        CloseHandle(thread);
        if (!ok) {
            logf(install, msgCreate, 0, err);
        }
        return ok;
    }
    DWORD err = GetLastError();
    if (err != ERROR_NO_TOKEN) {
        logf(install, msgOpenToken, 0, err);
        return 0;
    }
    logf(install, msgToken, msgAsProcess, 0);
    ok = CreateProcessW(0, cmd, 0, 0, 1, flags, 0, dir, &si->StartupInfo, pi);
    if (!ok) {
        logf(install, msgCreate, 0, GetLastError());
    }
    return ok;
}

// makeJob creates the job that holds the child and every process it
// starts, so that a cancel and the end of the action end them all; without
// it the action goes on with the child alone.
static HANDLE makeJob(MSIHANDLE install)
{
    HANDLE job = CreateJobObjectW(0, 0);
    if (!job) {
        logf(install, msgJob, 0, GetLastError());
        return 0;
    }
    JOBOBJECT_EXTENDED_LIMIT_INFORMATION limits;
    memset(&limits, 0, sizeof limits);
    limits.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
    if (!SetInformationJobObject(job, JobObjectExtendedLimitInformation, &limits, sizeof limits)) {
        logf(install, msgJob, 0, GetLastError());
        CloseHandle(job);
        return 0;
    }
    return job;
}

__declspec(dllexport) UINT WINAPI BuilderRun(MSIHANDLE install)
{
    HANDLE heap = GetProcessHeap();
    WCHAR *data = property(install, propData);
    if (!data) {
        data = property(install, propRun);
    }
    if (!data) {
        logf(install, msgNoData, 0, 0);
        return ERROR_INSTALL_FAILURE;
    }
    WCHAR *dir = 0, *cmd = 0;
    if (!parse(data, &dir, &cmd)) {
        logf(install, msgBadData, 0, 0);
        HeapFree(heap, 0, data);
        return ERROR_INSTALL_FAILURE;
    }
    logf(install, msgStart, dir ? dir : msgDefaultDir, 0);

    UINT result = ERROR_INSTALL_FAILURE;
    output o;
    memset(&o, 0, sizeof o);
    HANDLE writeEnd = 0;
    void *attributes = 0;
    STARTUPINFOEXW si;
    PROCESS_INFORMATION pi;
    memset(&si, 0, sizeof si);
    memset(&pi, 0, sizeof pi);

    o.buf = HeapAlloc(heap, 0, LINE_BUFFER);
    if (!o.buf) {
        logf(install, msgMemory, 0, 0);
        goto done;
    }
    if (!makePipe(install, &o.pipe, &writeEnd)) {
        goto done;
    }
    o.event = CreateEventW(0, 1, 0, 0);
    if (!o.event) {
        logf(install, msgEvent, 0, GetLastError());
        goto done;
    }
    {
        // The handle list limits what the child inherits to the pipe:
        // without it, every inheritable handle of the custom action server
        // goes along, including the pipe of another run action in flight.
        SIZE_T size = 0;
        InitializeProcThreadAttributeList(0, 1, 0, &size);
        attributes = HeapAlloc(heap, 0, size);
        if (!attributes || !InitializeProcThreadAttributeList(attributes, 1, 0, &size)) {
            logf(install, msgAttributes, 0, GetLastError());
            HeapFree(heap, 0, attributes);
            attributes = 0;
            goto done;
        }
        if (!UpdateProcThreadAttribute(attributes, 0, PROC_THREAD_ATTRIBUTE_HANDLE_LIST, &writeEnd, sizeof writeEnd, 0, 0)) {
            logf(install, msgAttributes, 0, GetLastError());
            goto done;
        }
    }
    si.StartupInfo.cb = sizeof si;
    si.StartupInfo.dwFlags = STARTF_USESTDHANDLES;
    si.StartupInfo.hStdOutput = writeEnd;
    si.StartupInfo.hStdError = writeEnd;
    si.StartupInfo.hStdInput = 0;
    si.lpAttributeList = attributes;
    o.job = makeJob(install);
    if (!start(install, cmd, dir, &si, &pi)) {
        goto done;
    }
    CloseHandle(writeEnd);
    writeEnd = 0;
    o.process = pi.hProcess;
    if (o.job && !AssignProcessToJobObject(o.job, pi.hProcess)) {
        logf(install, msgAssign, 0, GetLastError());
        CloseHandle(o.job);
        o.job = 0;
    }
    DWORD suspended = ResumeThread(pi.hThread);
    CloseHandle(pi.hThread);
    if (suspended == MAXDWORD_) {
        logf(install, msgResume, 0, GetLastError());
        stop(&o);
        goto done;
    }
    pump(install, &o);
    await(install, &o);
    DWORD exitCode = 1;
    GetExitCodeProcess(o.process, &exitCode);
    {
        WCHAR line[32];
        SIZE_T pos = 0;
        static const WCHAR sp[] = {' ', 0};
        wappend(line, &pos, msgExit);
        wappend(line, &pos, sp);
        wappendnum(line, &pos, exitCode);
        logw(install, line);
    }
    if (o.cancelled) {
        logf(install, msgCancel, 0, 0);
        result = ERROR_INSTALL_USEREXIT;
    } else {
        result = exitCode == 0 ? ERROR_SUCCESS : ERROR_INSTALL_FAILURE;
    }
done:
    if (o.process) {
        CloseHandle(o.process);
    }
    if (o.job) {
        // Closing the last handle of the job ends what is left in it.
        CloseHandle(o.job);
    }
    if (writeEnd) {
        CloseHandle(writeEnd);
    }
    if (o.pipe) {
        CloseHandle(o.pipe);
    }
    if (o.event) {
        CloseHandle(o.event);
    }
    if (attributes) {
        DeleteProcThreadAttributeList(attributes);
        HeapFree(heap, 0, attributes);
    }
    HeapFree(heap, 0, o.buf);
    HeapFree(heap, 0, cmd);
    HeapFree(heap, 0, data);
    return result;
}

// Permission grants. The deferred action BuilderGrantExec, its rollback
// BuilderGrantRollback and the commit action BuilderGrantCommit, all on the
// entry point BuilderGrant, get this CustomActionData:
//
//     <op>:<count><mode><ProductCode>[|]
//     <kind>|<mask>|<flags>|<account template>|<object>|<account>
//     ...
//
// The engine builds it, never an action that runs as the user (spec 17.7):
// the builder writes one type-51 action per grant, conditioned on its
// component being installed or repaired, that appends the entry to the
// property BuilderGrantData, which a type-51 action reset to the head line
// before, and the engine formats account and object in it. The template is
// escaped in the action, so it arrives as authored. A second type-51 action
// under the same condition appends `#` to BuilderGrantCount, which gives
// count: one `#` per entry. A line break in a formatted account or object
// would add lines, so the deferred action fails a grant whose number of
// lines differs.
//
// op is `+` (grant), `-` (rollback), `c` (commit) or `x` (the commit of the
// removal of the product, `x` before the head line without a count; it
// ignores the mode and the entries). mode
// is `i` on a first install and `m` in a maintenance install. A `|` after
// the ProductCode marks a per-user install (ALLUSERS unset); the user's SID
// is then the engine's UserSID, which a deferred action reads with
// MsiGetPropertyW: it is the account that started the session even after an
// action changed the session's property (FILE-12), unlike a value formatted
// into the data. kind is `f`
// (file or folder) or `s` (service), mask and flags are hexadecimal access
// rights and ACE flags, the account is a SID string, an SDDL alias such as
// NS, a service manager name such as LocalSystem, or a name for
// LookupAccountNameW (`.\name` is a local account), and the object is a path
// or a service name. The account comes last, so a `|` in it cannot move the
// object; an account with `|` or a control character is rejected.
//
// A grant adds the rights to the account's explicit allow ACE with the same
// inheritance flags, or adds such an ACE after the last explicit ACE, and
// keeps every other ACE, the inherited ones included. Before it changes an
// object it records the rights it adds, which the account did not have, and
// the account's SID in
// HKCU\Software\msi-builder\GrantRollback\<ProductCode> under the entry's
// number (the SID as REG_BINARY under the number with `s`; HKCU of the account the action runs as: LocalSystem's hive in a
// per-machine install). The rollback takes
// exactly those rights away again from that SID, without working out the
// account again, and the commit deletes the record, so rights
// that were there before the install survive a rollback.
//
// An account that comes from a property, such as [SVC_ACCOUNT], is
// remembered: the commit action stores the account of an entry whose
// template contains `[` under that template in
// HKLM\SOFTWARE\msi-builder\GrantAccounts\<ProductCode> in a per-machine
// install and in HKEY_USERS\<UserSID>\Software\msi-builder\GrantAccounts\
// <ProductCode> in a per-user one: a standard user's per-user install runs
// the action as that user (documented), who cannot write HKLM, and two users
// can install the same product per user. HKEY_USERS by SID, not HKCU, is
// the installing user's hive also when the action runs as LocalSystem (an
// elevated per-user install). A
// maintenance install (a repair from Apps & Features, a self-repair, a
// feature added later, a patch), whose command line does not have the
// property, grants to the remembered account when every property in the
// template is empty. Only an account that was never remembered is skipped
// there. The commit action of the removal of the product (`x`) deletes the
// key. Log lines read `BuilderGrant: ...`.

static const WCHAR rollbackKey[] = u"Software\\msi-builder\\GrantRollback\\";
static const WCHAR accountsKey[] = u"SOFTWARE\\msi-builder\\GrantAccounts\\";
static const WCHAR userAccountsKey[] = u"\\Software\\msi-builder\\GrantAccounts\\";
static const WCHAR propUserSid[] = u"UserSID";
static const WCHAR msgRegFailed[] = u"the product's registration cannot be read";
static const WCHAR msgRegNone[] = u"the product is not registered";
static const WCHAR msgRegNowhere[] = u"the product is not registered yet; the install's scope decides";
static const WCHAR msgRegMachine[] = u"the product is registered per machine";
static const WCHAR msgRegUser[] = u"the product is registered for the user";
static const WCHAR msgRegBoth[] = u"the product is registered per machine and for the user; the install's scope decides";
static const WCHAR msgNoContext[] = u"cannot tell where the product is registered; remembered accounts are not used";
static const WCHAR msgStillRegistered[] = u"the product is still registered in this scope; its remembered accounts stay";
static const WCHAR msgUserSid[] = u"the user's security identifier is missing or malformed; remembered accounts are not used";
static const WCHAR msgRemembered[] = u"the account is empty in a maintenance install; granting to the remembered account";
static const WCHAR msgRemember[] = u"cannot remember the account of";
static const WCHAR msgGrantData[] = u"malformed grant data";
static const WCHAR msgGrantLines[] = u"the grant data has more lines than grants: a formatted account or object contains a line break";
static const WCHAR msgNoAccount[] = u"no account to grant rights to";
static const WCHAR msgSkipEmpty[] = u"the account is empty in a maintenance install and none is remembered; skipped";
static const WCHAR msgAccountType[] = u"the account names a domain, a deleted account or nothing";
static const WCHAR msgBadAccount[] = u"the account contains | or a control character";
static const WCHAR msgFormat[] = u"cannot format";
static const WCHAR msgCondition[] = u"cannot evaluate the condition";
static const WCHAR msgBadObject[] = u"the object contains a control character";
static const WCHAR msgSid[] = u"cannot resolve the account";
static const WCHAR msgGetSec[] = u"cannot read the permissions of";
static const WCHAR msgSetSec[] = u"cannot write the permissions of";
static const WCHAR msgNoDacl[] = u"has no DACL, so everyone has full access; left as it is";
static const WCHAR msgRedirected[] = u"refused, reached through a junction, a symbolic link or a substituted drive, which leads to";
static const WCHAR msgAlready[] = u"already granted, left as it is";
static const WCHAR msgGranted[] = u"granted";
static const WCHAR msgRevoked[] = u"revoked";
static const WCHAR msgTo[] = u"to";
static const WCHAR msgFrom[] = u"from";
static const WCHAR msgAcl[] = u"cannot build the new DACL of";
static const WCHAR msgRecord[] = u"cannot record the grant for the rollback";
static const WCHAR msgNoMemory[] = u"out of memory";

// The service control manager's names of the built-in service accounts,
// which ServiceInstall.StartName uses: an account in a property that also
// names a service account comes in this form. LookupAccountNameW knows
// these accounts by other, localized names.
static const WCHAR *const serviceAccounts[][2] = {
    {u"LocalSystem", u"S-1-5-18"},
    {u".\\LocalSystem", u"S-1-5-18"},
    {u"NT AUTHORITY\\LocalService", u"S-1-5-19"},
    {u"NT AUTHORITY\\NetworkService", u"S-1-5-20"},
};

// HKEY_CURRENT_USER and HKEY_LOCAL_MACHINE, which winreg.h defines as the
// sign-extended 0x80000001 and 0x80000002.
#define HKCU_ ((HANDLE)(INTPTR)(int)0x80000001)
#define HKLM_ ((HANDLE)(INTPTR)(int)0x80000002)
#define HKU_ ((HANDLE)(INTPTR)(int)0x80000003)

static WCHAR wlower(WCHAR c)
{
    return c >= 'A' && c <= 'Z' ? (WCHAR)(c - 'A' + 'a') : c;
}

static int wieq(const WCHAR *a, const WCHAR *b)
{
    while (*a && wlower(*a) == wlower(*b)) {
        a++;
        b++;
    }
    return *a == *b;
}

// wbuf is a growing heap string.
typedef struct {
    WCHAR *s;
    SIZE_T len, cap;
} wbuf;

static int wbufAdd(wbuf *b, const WCHAR *src, SIZE_T n)
{
    if (b->len + n + 1 > b->cap) {
        SIZE_T cap = (b->len + n + 1) * 2;
        WCHAR *grown = b->s ? HeapReAlloc(GetProcessHeap(), 0, b->s, cap * sizeof(WCHAR)) : HeapAlloc(GetProcessHeap(), 0, cap * sizeof(WCHAR));
        if (!grown) {
            return 0;
        }
        b->s = grown;
        b->cap = cap;
    }
    memcpy(b->s + b->len, src, n * sizeof(WCHAR));
    b->len += n;
    b->s[b->len] = 0;
    return 1;
}

// format formats a template with MsiFormatRecordW into a heap string.
static WCHAR *format(MSIHANDLE install, const WCHAR *template)
{
    MSIHANDLE rec = MsiCreateRecord(0);
    if (!rec) {
        return 0;
    }
    WCHAR *out = 0;
    WCHAR probe[1];
    DWORD size = 0;
    MsiRecordSetStringW(rec, 0, template);
    UINT err = MsiFormatRecordW(install, rec, probe, &size);
    if (err == ERROR_MORE_DATA || err == ERROR_SUCCESS) {
        size++;
        out = HeapAlloc(GetProcessHeap(), HEAP_ZERO_MEMORY, (SIZE_T)size * sizeof(WCHAR));
        if (out && MsiFormatRecordW(install, rec, out, &size) != ERROR_SUCCESS) {
            HeapFree(GetProcessHeap(), 0, out);
            out = 0;
        }
    }
    MsiCloseHandle(rec);
    return out;
}

// trim removes leading and trailing spaces in place.
static WCHAR *trim(WCHAR *s)
{
    while (*s == ' ') {
        s++;
    }
    SIZE_T n = wlen(s);
    while (n && s[n - 1] == ' ') {
        s[--n] = 0;
    }
    return s;
}

static int hasControl(const WCHAR *s, int pipe)
{
    for (; *s; s++) {
        if (*s < 0x20 || (pipe && *s == '|')) {
            return 1;
        }
    }
    return 0;
}

// field cuts s at the next separator and returns the text after it, or 0.
static WCHAR *field(WCHAR *s, WCHAR sep)
{
    while (*s && *s != sep) {
        s++;
    }
    if (*s != sep) {
        return 0;
    }
    *s = 0;
    return s + 1;
}

// grantScope is where a product's remembered accounts are: HKLM in a
// per-machine install, the user's hive in HKEY_USERS in a per-user one.
// sid is 0 per machine; per user, bad tells that it was missing or not a SID
// string, and nothing is remembered or looked up.
typedef struct {
    const WCHAR *product;
    WCHAR *sid;
    int bad;
} grantScope;

// MSIINSTALLCONTEXT values of MsiGetProductInfoExW.
enum {
    ERROR_UNKNOWN_PRODUCT_ = 1605,
    CONTEXT_USERMANAGED = 1,
    CONTEXT_USERUNMANAGED = 2,
    CONTEXT_MACHINE = 4,
};

// registeredContexts tells where the engine has a product registered: a
// mask of CONTEXT_MACHINE and CONTEXT_USERUNMANAGED (for the user with the
// SID, managed or not), 0 for nowhere or, when a query fails for another
// reason than an unknown product, -1. A product can be installed once per
// machine and once per user at the same time (documented).
static int registeredContexts(const WCHAR *product, const WCHAR *sid)
{
    static const WCHAR stateProp[] = u"State";
    static const int contexts[] = {CONTEXT_MACHINE, CONTEXT_USERUNMANAGED, CONTEXT_USERMANAGED};
    int found = 0;
    for (int i = 0; i < 3; i++) {
        if (i && !sid) {
            break;
        }
        WCHAR state[16];
        DWORD n = 16;
        UINT err = MsiGetProductInfoExW(product, i ? sid : 0, contexts[i], stateProp, state, &n);
        if (!err) {
            found |= i ? CONTEXT_USERUNMANAGED : CONTEXT_MACHINE;
        } else if (err != ERROR_UNKNOWN_PRODUCT_) {
            return -1;
        }
    }
    return found;
}

// validSid tells whether s looks like a SID string (S-1-...), so that it
// cannot name another key below HKEY_USERS.
static int validSid(const WCHAR *s)
{
    if (s[0] != 'S' || s[1] != '-' || s[2] != '1' || s[3] != '-' || !s[4]) {
        return 0;
    }
    for (s += 4; *s; s++) {
        if ((*s < '0' || *s > '9') && *s != '-') {
            return 0;
        }
    }
    return 1;
}

// accountsPath returns the root and the key of the remembered accounts of a
// product, or 0.
static HANDLE accountsPath(wbuf *path, const grantScope *scope)
{
    if (scope->bad) {
        return 0;
    }
    if (!scope->sid) {
        return wbufAdd(path, accountsKey, wlen(accountsKey)) && wbufAdd(path, scope->product, wlen(scope->product)) ? HKLM_ : 0;
    }
    return wbufAdd(path, scope->sid, wlen(scope->sid)) && wbufAdd(path, userAccountsKey, wlen(userAccountsKey)) &&
                   wbufAdd(path, scope->product, wlen(scope->product))
               ? HKU_
               : 0;
}

// remembered returns the account an earlier install remembered for an
// account template, or 0.
static WCHAR *remembered(const grantScope *scope, const WCHAR *template)
{
    HANDLE heap = GetProcessHeap(), key = 0, root;
    wbuf path = {0, 0, 0};
    WCHAR *value = 0;
    if ((root = accountsPath(&path, scope)) != 0 && !RegOpenKeyExW(root, path.s, 0, KEY_QUERY_VALUE, &key)) {
        DWORD kind = 0, size = 0;
        if (!RegQueryValueExW(key, template, 0, &kind, 0, &size) && kind == REG_SZ && size) {
            // The zeroed extra character ends a value stored without one.
            value = HeapAlloc(heap, HEAP_ZERO_MEMORY, (SIZE_T)size + 2 * sizeof(WCHAR));
            if (value && (RegQueryValueExW(key, template, 0, &kind, (BYTE *)value, &size) || kind != REG_SZ)) {
                HeapFree(heap, 0, value);
                value = 0;
            }
        }
        RegCloseKey(key);
    }
    HeapFree(heap, 0, path.s);
    return value;
}

// literal returns the template without its [...] references, which is what
// it formats to when every property in it is empty.
static WCHAR *literal(const WCHAR *template)
{
    WCHAR *out = HeapAlloc(GetProcessHeap(), 0, (wlen(template) + 1) * sizeof(WCHAR));
    if (!out) {
        return 0;
    }
    SIZE_T n = 0;
    int depth = 0;
    for (; *template; template++) {
        if (*template == '[') {
            depth++;
        } else if (*template == ']' && depth) {
            depth--;
        } else if (!depth) {
            out[n++] = *template;
        }
    }
    out[n] = 0;
    return out;
}

static int hasBracket(const WCHAR *s)
{
    for (; *s; s++) {
        if (*s == '[') {
            return 1;
        }
    }
    return 0;
}

static WCHAR *hexfield(WCHAR *p, DWORD *value);

// grantEntry is one parsed entry of the grant data.
typedef struct {
    DWORD mask, flags;
    int type;
    WCHAR *template, *object, *account;
} grantEntry;

// parseEntry splits an entry in place. It returns 0 when it is malformed. A
// `|` in the object (an authored service name or a formatted directory
// property) moves the rest into the account, which then fails as an
// account with `|`.
static int parseEntry(WCHAR *entry, grantEntry *e)
{
    WCHAR *p = (entry[0] == 'f' || entry[0] == 's') && entry[1] == '|' ? hexfield(entry + 2, &e->mask) : 0;
    p = p ? hexfield(p, &e->flags) : 0;
    e->type = entry[0] == 's' ? SE_SERVICE : SE_FILE_OBJECT;
    e->template = p;
    e->object = p ? field(p, '|') : 0;
    e->account = e->object ? field(e->object, '|') : 0;
    return e->account && *e->template && e->mask && !(e->flags & ~AUTHORED_INHERIT_FLAGS);
}

// grantAccount returns the account an entry grants to: the formatted
// account, or, in a maintenance install (again) where the template formatted
// to its text without the [...] references, the account remembered for the
// template, which *kept holds for the caller to free. It returns 0, after a
// log line when log is set, when there is none: *fail tells whether that
// fails the install (a first install) or skips the entry.
static WCHAR *grantAccount(MSIHANDLE install, const grantScope *scope, int again, const grantEntry *e, WCHAR **kept, int log, int *fail)
{
    const WCHAR *template = e->template;
    WCHAR *name = trim(e->account), *lit = hasBracket(template) ? literal(template) : 0;
    *kept = 0;
    *fail = 1;
    // `.\[SVC_USER]` with an empty property is as unset as `[SVC_USER]`.
    if (*name && lit && wieq(name, trim(lit))) {
        *name = 0;
    }
    HeapFree(GetProcessHeap(), 0, lit);
    if (!*name && again && (*kept = remembered(scope, template)) != 0 && *trim(*kept)) {
        // A property set on the command line of the first install is
        // not there in a repair from Apps & Features, a self-repair or
        // a patch, which replace files and add components.
        name = trim(*kept);
        if (log) {
            logft(install, tmplGrant, msgRemembered, e->object, 0);
        }
    }
    if (!*name) {
        *fail = !again;
        if (log) {
            logft(install, tmplGrant, again ? msgSkipEmpty : msgNoAccount, e->object, 0);
        }
        return 0;
    }
    if (hasControl(name, 1)) {
        if (log) {
            logft(install, tmplGrant, msgBadAccount, name, 0);
        }
        return 0;
    }
    return name;
}

// hexfield parses hexadecimal digits up to the next `|`, which it replaces
// with 0, and returns the text after it, or 0 when the field is malformed.
static WCHAR *hexfield(WCHAR *p, DWORD *value)
{
    DWORD v = 0;
    int digits = 0;
    for (; *p && *p != '|'; p++, digits++) {
        WCHAR c = wlower(*p);
        if (digits == 8) {
            return 0;
        }
        if (c >= '0' && c <= '9') {
            v = v << 4 | (DWORD)(c - '0');
        } else if (c >= 'a' && c <= 'f') {
            v = v << 4 | (DWORD)(c - 'a' + 10);
        } else {
            return 0;
        }
    }
    if (*p != '|' || digits == 0) {
        return 0;
    }
    *value = v;
    return p + 1;
}

// resolveSid returns the account's SID in a heap buffer.
static void *resolveSid(MSIHANDLE install, const WCHAR *account)
{
    HANDLE heap = GetProcessHeap();
    for (SIZE_T i = 0; i < sizeof serviceAccounts / sizeof serviceAccounts[0]; i++) {
        if (wieq(account, serviceAccounts[i][0])) {
            account = serviceAccounts[i][1];
            break;
        }
    }
    SIZE_T n = wlen(account);
    void *converted = 0;
    if (((n > 2 && account[0] == 'S' && account[1] == '-') || n == 2) && ConvertStringSidToSidW(account, &converted)) {
        DWORD size = GetLengthSid(converted);
        void *sid = HeapAlloc(heap, 0, size);
        if (sid) {
            memcpy(sid, converted, size);
        }
        LocalFree(converted);
        return sid;
    }
    // `.\name` is a local account: LookupAccountNameW takes it as
    // <computer>\name, while a bare name could also find a domain account.
    WCHAR *qualified = 0;
    const WCHAR *name = account;
    if (account[0] == '.' && account[1] == '\\') {
        WCHAR computer[64];
        DWORD len = sizeof computer / sizeof computer[0];
        if (!GetComputerNameW(computer, &len)) {
            logft(install, tmplGrant, msgSid, account, GetLastError());
            return 0;
        }
        qualified = HeapAlloc(heap, 0, ((SIZE_T)len + n + 1) * sizeof(WCHAR));
        if (!qualified) {
            return 0;
        }
        SIZE_T pos = 0;
        qualified[0] = 0;
        wappend(qualified, &pos, computer);
        wappend(qualified, &pos, account + 1);
        name = qualified;
    }
    DWORD sidSize = 0, domainSize = 0;
    int use = 0;
    void *sid = 0;
    WCHAR *domain = 0;
    LookupAccountNameW(0, name, 0, &sidSize, 0, &domainSize, &use);
    DWORD err = GetLastError();
    if (err != ERROR_INSUFFICIENT_BUFFER || !sidSize) {
        logft(install, tmplGrant, msgSid, account, err);
        goto done;
    }
    sid = HeapAlloc(heap, 0, sidSize);
    domain = HeapAlloc(heap, 0, ((SIZE_T)domainSize + 1) * sizeof(WCHAR));
    if (!sid || !domain || !LookupAccountNameW(0, name, sid, &sidSize, domain, &domainSize, &use)) {
        logft(install, tmplGrant, msgSid, account, GetLastError());
        HeapFree(heap, 0, sid);
        sid = 0;
    } else if (use == SidTypeDomain || use == SidTypeDeletedAccount || use == SidTypeInvalid || use == SidTypeUnknown) {
        logft(install, tmplGrant, msgAccountType, account, 0);
        HeapFree(heap, 0, sid);
        sid = 0;
    }
done:
    HeapFree(heap, 0, domain);
    HeapFree(heap, 0, qualified);
    return sid;
}

// rebuild copies dacl into *out with rights changed: on grant, mask is added
// to the account's explicit allow ACE with these flags, or a new ACE goes
// after the last explicit ACE, where the canonical order puts explicit allow
// ACEs, and *changed holds the rights the account did not have; on revoke,
// mask is taken from that ACE, which goes away once it has no rights left.
// *out stays 0 when nothing changes. It returns 0 on an error.
static int rebuild(ACL *dacl, void *sid, DWORD mask, DWORD flags, int grant, ACL **out, DWORD *changed)
{
    long found = -1;
    DWORD insertAt = dacl->AceCount, have = 0;
    *out = 0;
    *changed = 0;
    for (DWORD i = 0; i < dacl->AceCount; i++) {
        ACE_HEADER *ace;
        if (!GetAce(dacl, i, (void **)&ace)) {
            return 0;
        }
        if (ace->AceFlags & INHERITED_ACE) {
            if (insertAt == dacl->AceCount) {
                insertAt = i;
            }
            continue;
        }
        if (found < 0 && ace->AceType == ACCESS_ALLOWED_ACE_TYPE && (ace->AceFlags & VALID_INHERIT_FLAGS) == flags &&
            EqualSid(&((ACCESS_ALLOWED_ACE *)ace)->SidStart, sid)) {
            found = (long)i;
            have = ((ACCESS_ALLOWED_ACE *)ace)->Mask;
        }
    }
    *changed = grant ? mask & ~have : mask & have;
    if (!*changed) {
        return 1;
    }
    DWORD size = dacl->AclSize + (found < 0 ? sizeof(ACCESS_ALLOWED_ACE) + GetLengthSid(sid) : 0);
    DWORD revision = dacl->AclRevision > ACL_REVISION ? dacl->AclRevision : ACL_REVISION;
    ACL *acl = HeapAlloc(GetProcessHeap(), HEAP_ZERO_MEMORY, size);
    if (!acl || !InitializeAcl(acl, size, revision)) {
        HeapFree(GetProcessHeap(), 0, acl);
        return 0;
    }
    int ok = 1;
    for (DWORD i = 0; ok && i <= dacl->AceCount; i++) {
        if (found < 0 && i == insertAt) {
            ok = AddAccessAllowedAceEx(acl, revision, flags, mask, sid);
        }
        if (!ok || i == dacl->AceCount) {
            break;
        }
        ACE_HEADER *ace;
        if (!GetAce(dacl, i, (void **)&ace)) {
            ok = 0;
            break;
        }
        if ((long)i != found) {
            ok = AddAce(acl, revision, MAXDWORD_, ace, ace->AceSize);
            continue;
        }
        DWORD rights = grant ? have | mask : have & ~mask;
        if (rights == 0) {
            continue;
        }
        ACCESS_ALLOWED_ACE *copy;
        ok = AddAce(acl, revision, MAXDWORD_, ace, ace->AceSize) && GetAce(acl, acl->AceCount - 1, (void **)&copy);
        if (ok) {
            copy->Mask = rights;
        }
    }
    if (!ok) {
        HeapFree(GetProcessHeap(), 0, acl);
        return 0;
    }
    *out = acl;
    return 1;
}

static WCHAR *longPath(const WCHAR *path);
static WCHAR *finalPathOf(HANDLE file);
static int samePath(const WCHAR *a, const WCHAR *b);

// openGrantObject opens the file or folder of a grant for reading and
// writing its DACL and checks that the handle reached it at that very path:
// its final path, after every junction, symbolic link and substituted drive
// is resolved, must be the long form of the path. Otherwise a link in place
// of the object or of a folder above it would lead the grant elsewhere. A
// volume mounted in a folder is refused the same way when the final path
// names it by another drive letter. It returns 0 with *file set, a Windows
// error, or MAXDWORD_ after it logged the refusal.
static DWORD openGrantObject(MSIHANDLE install, const WCHAR *object, HANDLE *file)
{
    // Share access for everyone, so that the system can propagate inheritable
    // ACEs to the children (SetSecurityInfo), and explicit rights: a
    // MAXIMUM_ALLOWED handle does not propagate.
    HANDLE h = CreateFileW(object, READ_CONTROL_ | WRITE_DAC_, FILE_SHARE_ALL, 0, OPEN_EXISTING, FILE_FLAG_BACKUP_SEMANTICS, 0);
    if (h == INVALID_HANDLE_VALUE) {
        return GetLastError();
    }
    WCHAR *expected = longPath(object), *final = finalPathOf(h);
    if (expected && final && samePath(expected, final)) {
        HeapFree(GetProcessHeap(), 0, expected);
        HeapFree(GetProcessHeap(), 0, final);
        *file = h;
        return 0;
    }
    static const WCHAR unknown[] = u"a path that is not on a local drive", space[] = u" ";
    const WCHAR *to = final ? final : unknown;
    wbuf text = {0, 0, 0};
    if (wbufAdd(&text, msgRedirected, wlen(msgRedirected)) && wbufAdd(&text, space, 1) && wbufAdd(&text, to, wlen(to))) {
        logft(install, tmplGrant, object, text.s, 0);
    } else {
        logft(install, tmplGrant, object, msgRedirected, 0);
    }
    HeapFree(GetProcessHeap(), 0, text.s);
    HeapFree(GetProcessHeap(), 0, expected);
    HeapFree(GetProcessHeap(), 0, final);
    CloseHandle(h);
    return MAXDWORD_;
}

// applyEntry grants (or, on rollback, revokes) the rights of one entry to
// account. On a grant, the rights it is about to add are recorded under
// valueName in key (keyErr is why key is 0) before the object changes. It
// returns 0 on an error.
static int applyEntry(MSIHANDLE install, grantEntry *e, const WCHAR *account, int grant, HANDLE key, long keyErr, const WCHAR *valueName)
{
    HANDLE heap = GetProcessHeap();
    DWORD mask = e->mask, flags = e->flags;
    int type = e->type;
    WCHAR *object = e->object;
    void *sid = 0;
    // The SID is recorded under the entry's number with `s`; valueName
    // has at most ten digits.
    WCHAR sidName[16];
    SIZE_T nameLen = wlen(valueName);
    memcpy(sidName, valueName, nameLen * sizeof(WCHAR));
    sidName[nameLen] = 's';
    sidName[nameLen + 1] = 0;
    if (!grant) {
        // Only what this install added is taken away, from the account it
        // was granted to: the recorded SID, never the account worked out
        // again, which in a per-user install comes from a key the user can
        // write.
        DWORD added = 0, size = sizeof added, kind = 0;
        if (!key || RegQueryValueExW(key, valueName, 0, &kind, (BYTE *)&added, &size) || kind != REG_DWORD || !added) {
            return 1;
        }
        mask = added;
        BYTE recorded[68]; // a SID has at most 15 subauthorities
        size = sizeof recorded;
        if (RegQueryValueExW(key, sidName, 0, &kind, recorded, &size) || kind != REG_BINARY || size < 8 || recorded[0] != 1 ||
            recorded[1] > 15 || size != 8 + 4 * (DWORD)recorded[1] || (sid = HeapAlloc(heap, 0, size)) == 0) {
            return 1;
        }
        memcpy(sid, recorded, size);
    }
    // A folder comes as [DIRECTORY], which ends in a backslash; a drive root
    // such as C:\ keeps it.
    SIZE_T objLen = wlen(object);
    if (objLen > 3 && object[objLen - 1] == '\\') {
        object[objLen - 1] = 0;
    }

    int ok = 0;
    if (grant) {
        sid = resolveSid(install, account);
    }
    void *sd = 0;
    ACL *dacl = 0, *acl = 0;
    HANDLE file = 0;
    if (!sid) {
        goto done;
    }
    DWORD err;
    if (type == SE_FILE_OBJECT) {
        // A file or folder is read and written by a handle whose path is
        // checked, never by name: a junction in place of a folder above it
        // would send LocalSystem's write to another tree.
        if ((err = openGrantObject(install, object, &file)) != 0) {
            if (err != MAXDWORD_) {
                logft(install, tmplGrant, msgGetSec, object, err);
            }
            goto done;
        }
        err = GetSecurityInfo(file, type, DACL_SECURITY_INFORMATION, 0, 0, &dacl, 0, &sd);
    } else {
        err = GetNamedSecurityInfoW(object, type, DACL_SECURITY_INFORMATION, 0, 0, &dacl, 0, &sd);
    }
    if (err) {
        logft(install, tmplGrant, msgGetSec, object, err);
        goto done;
    }
    if (!dacl) {
        logft(install, tmplGrant, object, msgNoDacl, 0);
        ok = 1;
        goto done;
    }
    DWORD changed = 0;
    if (!rebuild(dacl, sid, mask, flags, grant, &acl, &changed)) {
        logft(install, tmplGrant, msgAcl, object, GetLastError());
        goto done;
    }
    if (!acl) {
        if (grant) {
            logft(install, tmplGrant, object, msgAlready, 0);
        }
        ok = 1;
        goto done;
    }
    if (grant) {
        long regErr = key ? RegSetValueExW(key, valueName, 0, REG_DWORD, (const BYTE *)&changed, sizeof changed) : keyErr;
        if (!regErr) {
            regErr = RegSetValueExW(key, sidName, 0, REG_BINARY, (const BYTE *)sid, GetLengthSid(sid));
        }
        if (regErr) {
            logft(install, tmplGrant, msgRecord, object, (DWORD)regErr);
            goto done;
        }
    }
    // A file or folder keeps its protection state: a protected DACL stays
    // protected, and an unprotected one keeps inheriting from its parent.
    DWORD info = DACL_SECURITY_INFORMATION;
    if (type == SE_FILE_OBJECT) {
        unsigned short control = 0;
        DWORD revision = 0;
        if (!GetSecurityDescriptorControl(sd, &control, &revision)) {
            logft(install, tmplGrant, msgGetSec, object, GetLastError());
            goto done;
        }
        info |= control & SE_DACL_PROTECTED ? PROTECTED_DACL_SECURITY_INFORMATION : UNPROTECTED_DACL_SECURITY_INFORMATION;
    }
    err = file ? SetSecurityInfo(file, type, info, 0, 0, acl, 0) : SetNamedSecurityInfoW(object, type, info, 0, 0, acl, 0);
    if (err) {
        logft(install, tmplGrant, msgSetSec, object, err);
        goto done;
    }
    logft(install, tmplGrant, grant ? msgGranted : msgRevoked, object, 0);
    WCHAR *sidText = 0;
    if (account) {
        logft(install, tmplGrant, msgTo, account, 0);
    } else if (ConvertSidToStringSidW(sid, &sidText)) {
        logft(install, tmplGrant, msgFrom, sidText, 0);
        LocalFree(sidText);
    }
    ok = 1;
done:
    if (sd) {
        LocalFree(sd);
    }
    if (file) {
        CloseHandle(file);
    }
    HeapFree(heap, 0, acl);
    HeapFree(heap, 0, sid);
    return ok;
}

// rememberAccount stores an account under its template. The commit action
// does it, so a failed install or repair leaves the account that was
// remembered before. It never fails: without it, only a maintenance install
// without the property skips the grant.
static void rememberAccount(MSIHANDLE install, const grantScope *scope, const WCHAR *template, const WCHAR *account)
{
    HANDLE heap = GetProcessHeap(), key = 0, root;
    wbuf path = {0, 0, 0};
    if (scope->bad) {
        return; // logged once
    }
    if ((root = accountsPath(&path, scope)) == 0) {
        logft(install, tmplGrant, msgNoMemory, 0, 0);
        HeapFree(heap, 0, path.s);
        return;
    }
    long err = RegCreateKeyExW(root, path.s, 0, 0, 0, KEY_SET_VALUE, 0, &key, 0);
    if (!err) {
        err = RegSetValueExW(key, template, 0, REG_SZ, (const BYTE *)account, (DWORD)((wlen(account) + 1) * sizeof(WCHAR)));
        RegCloseKey(key);
    }
    if (err) {
        logft(install, tmplGrant, msgRemember, template, (DWORD)err);
    }
    HeapFree(heap, 0, path.s);
}

__declspec(dllexport) UINT WINAPI BuilderGrant(MSIHANDLE install)
{
    HANDLE heap = GetProcessHeap();
    WCHAR *data = property(install, propData);
    WCHAR op = data ? data[0] : 0;
    WCHAR *head = op && op != 'x' && data[1] == ':' ? data + 2 : 0;
    DWORD count = 0, lines = 0;
    for (; head && *head == '#'; head++) {
        count++;
    }
    int again = head && *head == 'm';
    if (!data || (op != '+' && op != '-' && op != 'c' && op != 'x') || (op != 'x' && (!head || (*head != 'i' && !again)))) {
        logft(install, tmplGrant, msgGrantData, 0, 0);
        HeapFree(heap, 0, data);
        return data ? ERROR_INSTALL_FAILURE : ERROR_SUCCESS;
    }
    if (op == 'x' && (data[1] == 'i' || data[1] == 'm')) {
        head = data + 1; // the head line of the data, without a count
    }
    WCHAR *product = head ? head + 1 : data + 1;
    WCHAR *entries = field(product, '\n');
    grantScope scope = {product, 0, 0};
    int perUser = field(product, '|') != 0;
    int context = 0;
    const WCHAR *why = 0;
    if (again || op == 'c' || op == 'x') {
        // Where the engine registered the product decides, not ALLUSERS,
        // which a user may pass to an elevated repair and an impersonated
        // action may change: a per-machine product must never read or write
        // accounts in the user's hive. A maintenance install whose product
        // is registered nowhere (or cannot be told) uses no remembered
        // account; the commit of a first install falls back to the marker
        // only when the product is registered nowhere; the removal's commit
        // runs after the product was unregistered, so the marker names the
        // scope, and nothing is deleted while the product is still
        // registered there.
        // When the product is registered in both contexts, the engine set
        // ALLUSERS to the one it works on, so the marker tells them apart.
        WCHAR *user = property(install, propUserSid);
        context = registeredContexts(product, user && validSid(user) ? user : 0);
        HeapFree(heap, 0, user);
        if (op == 'c') {
            logft(install, tmplGrant, context < 0 ? msgRegFailed : context == 0 ? (again ? msgRegNone : msgRegNowhere) : context == CONTEXT_MACHINE ? msgRegMachine : context == CONTEXT_USERUNMANAGED ? msgRegUser : msgRegBoth, 0, 0);
        }
        if (context > 0 && context != (CONTEXT_MACHINE | CONTEXT_USERUNMANAGED) && op != 'x') {
            perUser = context == CONTEXT_USERUNMANAGED;
        } else if (op != 'x' && (again || context < 0) && context != (CONTEXT_MACHINE | CONTEXT_USERUNMANAGED)) {
            scope.bad = 1;
            why = msgNoContext;
        }
    }
    if (op == 'x' && context > 0 && (context & (perUser ? CONTEXT_USERUNMANAGED : CONTEXT_MACHINE))) {
        scope.bad = 1; // another install of the product still uses the accounts
        why = msgStillRegistered;
    }
    if (perUser && op != '-' && !scope.bad) {
        // Per user: the engine's UserSID names the hive.
        scope.sid = property(install, propUserSid);
        scope.bad = !scope.sid;
    }
    if (!why && (scope.bad || (scope.sid && !validSid(scope.sid)))) {
        scope.bad = 1;
        why = msgUserSid;
    }
    if (why && op != '-') {
        logft(install, tmplGrant, why, 0, 0);
    }
    if (!*product) {
        // An empty ProductCode would name every product's key below.
        logft(install, tmplGrant, msgGrantData, 0, 0);
        HeapFree(heap, 0, scope.sid);
        HeapFree(heap, 0, data);
        return op == '+' ? ERROR_INSTALL_FAILURE : ERROR_SUCCESS;
    }
    for (WCHAR *p = entries; p && *p; p++) {
        lines += *p == '\n';
    }
    if (entries && *entries) {
        lines++;
    }
    if (op != 'x' && lines != count) {
        // A line break in a formatted account or object added lines.
        logft(install, tmplGrant, msgGrantLines, 0, 0);
        HeapFree(heap, 0, scope.sid);
        HeapFree(heap, 0, data);
        return op == '+' ? ERROR_INSTALL_FAILURE : ERROR_SUCCESS;
    }
    wbuf path = {0, 0, 0};
    if (op == 'x') {
        HANDLE root = accountsPath(&path, &scope);
        if (root) {
            RegDeleteTreeW(root, path.s);
        }
        HeapFree(heap, 0, path.s);
        HeapFree(heap, 0, scope.sid);
        HeapFree(heap, 0, data);
        return ERROR_SUCCESS;
    }
    if (!wbufAdd(&path, rollbackKey, wlen(rollbackKey)) || !wbufAdd(&path, product, wlen(product))) {
        HeapFree(heap, 0, scope.sid);
        HeapFree(heap, 0, data);
        return op == '+' ? ERROR_INSTALL_FAILURE : ERROR_SUCCESS;
    }
    HANDLE key = 0;
    long keyErr = 0;
    UINT result = ERROR_SUCCESS;
    if (op == '+') {
        // A record left by an install that never reached its commit (or
        // ran with rollback disabled, which skips commit actions too) is
        // stale.
        RegDeleteTreeW(HKCU_, path.s);
        if (entries && (keyErr = RegCreateKeyExW(HKCU_, path.s, 0, 0, 0, KEY_QUERY_VALUE | KEY_SET_VALUE, 0, &key, 0)) != 0) {
            key = 0;
        }
    } else if (op == '-' && RegOpenKeyExW(HKCU_, path.s, 0, KEY_QUERY_VALUE, &key)) {
        key = 0;
    }
    DWORD index = 0;
    for (WCHAR *entry = entries; entry && *entry; index++) {
        WCHAR *next = field(entry, '\n'), *kept = 0, *account = 0;
        WCHAR name[12];
        SIZE_T pos = 0;
        grantEntry e;
        int fail = 1, parsed = 0;
        wappendnum(name, &pos, index);
        if (!parseEntry(entry, &e)) {
            logft(install, tmplGrant, msgGrantData, 0, 0);
        } else if (!*e.object || hasControl(e.object, 0)) {
            logft(install, tmplGrant, msgBadObject, e.object, 0);
        } else {
            parsed = 1;
            if (op != '-') {
                account = grantAccount(install, &scope, again, &e, &kept, op == '+', &fail);
            }
        }
        // The commit remembers the accounts that came from properties; a
        // rollback undoes every grant it can and never fails.
        if (account && op == 'c') {
            if (hasBracket(e.template)) {
                rememberAccount(install, &scope, e.template, account);
            }
        } else if (account || (op == '-' && parsed)) {
            // The rollback takes the account from the record.
            fail = !applyEntry(install, &e, account, op == '+', key, keyErr, name);
        }
        HeapFree(heap, 0, kept);
        if (fail && op == '+') {
            result = ERROR_INSTALL_FAILURE;
            break;
        }
        entry = next;
    }
    if (key) {
        RegCloseKey(key);
    }
    if (op != '+') {
        RegDeleteTreeW(HKCU_, path.s);
    }
    HeapFree(heap, 0, path.s);
    HeapFree(heap, 0, scope.sid);
    HeapFree(heap, 0, data);
    return result;
}

// Folder removal. The rollback, deferred and commit actions
// BuilderRemoveFoldersUndo, BuilderRemoveFoldersExec and
// BuilderRemoveFoldersCommit, all on the entry point BuilderRemoveFoldersExec,
// run after RemoveFiles, when component states and directory paths are
// known, and get this CustomActionData:
//
//     <op><scope><ProductCode>:<count>
//     n<the number of s and p lines>
//     s<standard folder property>|<its value>
//     ...
//     p<a directory of the package>
//     ...
//     f<property>|<the folder it names>
//     ...
//
// The engine builds it, never an action that runs as the user (spec 17.10):
// type-51 actions right before the three actions reset the property
// BuilderRemoveFoldersData to the s and p lines, which the engine formats,
// and append an f line for each entry whose condition holds; a second
// type-51 action under the same condition appends `#` to
// BuilderRemoveFoldersCount, which gives count, and the builder writes the
// number of s and p lines into the n line, so a line break in a formatted
// value, which would add lines, makes the data malformed. op is u (rollback), m (deferred), d (deferred with
// rollback disabled, which skips commit actions: it deletes right away) or
// c (commit); scope is a when the whole product is removed, else k.
//
// The deferred action refuses a folder that is not one an application
// could own (a drive root, a folder that is or holds a standard folder or
// the profiles folder, a profile, a folder in the Windows folder, a reparse
// point, a folder reached through one, and unless scope is a, a folder that
// is or holds a directory of the package). It moves each accepted folder by
// handle into X:\BuilderRemoveFolders.<token>\<n>, n being the number of
// its f line, a new folder with a protected access list for LocalSystem,
// Administrators and its owner (its name is no secret: anyone can list the
// volume root; what makes it safe is that the rollback and commit sides
// only use it after checking that owner and access list). The random token
// and the final path of each moved folder are recorded in
// HKCU\Software\msi-builder\RemoveFolders\<ProductCode> (LocalSystem's hive
// in a per-machine install) before the move, so the rollback moves the
// folders recorded there back and the commit deletes them; both delete the
// record. Deleting opens every entry relative to the handle of its folder
// without following a junction or a symbolic link, which it removes as a
// link, so a folder swapped for a junction after the check cannot
// redirect it. What it cannot do is logged as `BuilderRemoveFolders: ...`
// and left in place; the actions never fail the installation.

static const WCHAR tmplRemove[] = u"BuilderRemoveFolders: [1]";
static const WCHAR msgRemovePackage[] = u"refused, it is or holds a folder of the package, which goes only when the whole product is removed:";
static const WCHAR backupPrefix[] = u"BuilderRemoveFolders.";
static const WCHAR msgRemoveData[] = u"malformed folder removal data";
static const WCHAR msgRemoveLines[] = u"the folder removal data has more lines than entries: a formatted folder contains a line break; nothing removed";
static const WCHAR msgRemoveRecord[] = u"cannot record the folders for the rollback; nothing removed";
static const WCHAR removeRecordKey[] = u"Software\\msi-builder\\RemoveFolders\\";
static const WCHAR removeRecordToken[] = u"Token";
static const WCHAR removeLeftKey[] = u"Software\\msi-builder\\RemoveFoldersLeft\\";
static const WCHAR removeLeftProduct[] = u"ProductCode";
static const WCHAR windowsFolder[] = u"WindowsFolder";
static const WCHAR msgRemoveNotAdminExec[] =
    u"nothing removed: the account that started the session is no administrator, or its groups cannot be read";
static const WCHAR msgRemoveEmpty[] = u"the property is empty; nothing removed";
static const WCHAR msgRemoveMissing[] = u"the folder does not exist; nothing removed";
static const WCHAR msgRemovePath[] = u"refused, not a full local folder path";
static const WCHAR msgRemoveRoot[] = u"refused, a drive root";
static const WCHAR msgRemoveStandard[] = u"refused, it is or holds the standard folder";
static const WCHAR msgRemoveWindows[] = u"refused, inside the Windows folder";
static const WCHAR msgRemoveReparse[] = u"refused, a junction or another reparse point";
static const WCHAR msgRemoveRedirected[] = u"refused, reached through a junction, a symbolic link or a substituted drive, which leads to";
static const WCHAR msgRemoveProfiles[] = u"refused, it is a user profile, the profiles folder or a folder that holds one";
static const WCHAR msgRemoveOtherProfile[] = u"refused, inside another user's profile";
static const WCHAR msgRemoveSharedProfile[] = u"refused, inside the Public or Default profile";
static const WCHAR msgRemoveNoProfiles[] = u"refused, the user profiles cannot be read";
static const WCHAR profileListKey[] = u"SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion\\ProfileList";
static const WCHAR profilesDirectory[] = u"ProfilesDirectory";
static const WCHAR profileImagePath[] = u"ProfileImagePath";
static const WCHAR msgRemoving[] = u"to remove";
static const WCHAR adminsSid[] = u"S-1-5-32-544";
static const WCHAR msgRemoveMoved[] = u"moved aside";
static const WCHAR msgRemoveChanged[] = u"left in place, it is no longer the folder that was checked";
static const WCHAR msgRemoveBusy[] = u"left in place, it cannot be moved (a file in it may be in use)";
static const WCHAR msgRemoveBackup[] = u"left in place, the backup folder at the root of its volume cannot be created or has no access list of its own (a FAT volume)";
static const WCHAR msgRemoveVolume[] = u"left in place, it is on a volume mounted in a folder";
static const WCHAR msgRemoveRestored[] = u"restored";
static const WCHAR msgRemoveRestore[] = u"cannot be restored, it stays in";
static const WCHAR msgRemoveStale[] = u"was moved aside by a removal that reached neither its rollback nor its commit, it stays in";
static const WCHAR msgRemoveKept[] = u"its original path is kept in HKEY_CURRENT_USER (HKEY_USERS\\S-1-5-18 for LocalSystem) under";
static const WCHAR msgRemoveNotKept[] = u"its original path cannot be kept in the registry";
static const WCHAR msgRemoveDeleted[] = u"deleted";
static const WCHAR msgRemoveLeft[] = u"not everything could be deleted, the rest stays in";
static const WCHAR backupSddl[] = u"D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;OW)";

// removeData is the parsed CustomActionData: the s, p and f lines, cut at
// their ends, and what the head says.
typedef struct {
    WCHAR **standard, **package, **folders;
    DWORD standards, packages, count;
    int all;
    WCHAR *own; // the engine's UserSID
} removeData;

// wstrdup copies a string into a heap buffer with room for extra more
// characters.
static WCHAR *wstrdup(const WCHAR *s, SIZE_T extra)
{
    SIZE_T n = wlen(s);
    WCHAR *out = HeapAlloc(GetProcessHeap(), 0, (n + extra + 1) * sizeof(WCHAR));
    if (out) {
        memcpy(out, s, (n + 1) * sizeof(WCHAR));
    }
    return out;
}

// longPath returns the long form of an existing path with a trailing
// backslash, or 0 when it does not exist.
static WCHAR *longPath(const WCHAR *path)
{
    DWORD n = GetLongPathNameW(path, 0, 0);
    if (!n) {
        return 0;
    }
    WCHAR *out = HeapAlloc(GetProcessHeap(), 0, ((SIZE_T)n + 2) * sizeof(WCHAR));
    if (!out) {
        return 0;
    }
    DWORD got = GetLongPathNameW(path, out, n);
    if (!got || got >= n) {
        HeapFree(GetProcessHeap(), 0, out);
        return 0;
    }
    if (out[got - 1] != '\\') {
        out[got++] = '\\';
        out[got] = 0;
    }
    return out;
}

// below reports whether path is folder or a folder below it, ignoring case.
// Both end with a backslash.
static int below(const WCHAR *path, const WCHAR *folder)
{
    SIZE_T n = wlen(folder), m = wlen(path);
    return n <= m && CompareStringOrdinal(path, (int)n, folder, (int)n, 1) == CSTR_EQUAL;
}

// plainPath reports whether path is a full local path, C:\ and folder names,
// without the . and .. segments, a forward slash or a control character.
static int plainPath(const WCHAR *path)
{
    WCHAR d = wlower(path[0]);
    if (d < 'a' || d > 'z' || path[1] != ':' || path[2] != '\\') {
        return 0;
    }
    for (const WCHAR *p = path + 2; *p; p++) {
        if (*p < 0x20 || *p == '/' || *p == ':' || *p == '*' || *p == '?' || *p == '"' || *p == '<' || *p == '>' || *p == '|') {
            return 0;
        }
        if (*p == '\\') {
            const WCHAR *seg = p + 1;
            SIZE_T dots = 0;
            while (seg[dots] == '.') {
                dots++;
            }
            if ((seg[0] == '\\') || (dots && (seg[dots] == '\\' || seg[dots] == 0))) {
                return 0;
            }
        }
        // Windows drops a trailing space or dot of a name, so
        // "C:\Program Files \" opens Program Files but compares unlike it.
        if ((*p == ' ' || *p == '.') && (p[1] == '\\' || p[1] == 0)) {
            return 0;
        }
    }
    return 1;
}

// finalPath returns the path an existing folder has after every junction,
// symbolic link and substituted drive on the way is resolved, as C:\...\
// with a trailing backslash, or 0 when it cannot be opened or is not on a
// local drive.
static WCHAR *finalPathOf(HANDLE file)
{
    WCHAR *out = 0;
    DWORD n = GetFinalPathNameByHandleW(file, 0, 0, 0);
    if (n) {
        out = HeapAlloc(GetProcessHeap(), 0, ((SIZE_T)n + 2) * sizeof(WCHAR));
        DWORD got = out ? GetFinalPathNameByHandleW(file, out, n, 0) : 0;
        // \\?\C:\... for a local drive; \\?\UNC\... is a network share.
        if (got < 7 || got >= n || out[0] != '\\' || out[1] != '\\' || out[2] != '?' || out[3] != '\\' || out[5] != ':' || !plainPath(out + 4)) {
            HeapFree(GetProcessHeap(), 0, out);
            out = 0;
        } else {
            moveDown(out, out + 4, ((SIZE_T)got - 3) * sizeof(WCHAR));
            got -= 4;
            if (out[got - 1] != '\\') {
                out[got++] = '\\';
                out[got] = 0;
            }
        }
    }
    return out;
}

static WCHAR *finalPath(const WCHAR *path)
{
    HANDLE file = CreateFileW(path, FILE_READ_ATTRIBUTES, FILE_SHARE_ALL, 0, OPEN_EXISTING, FILE_FLAG_BACKUP_SEMANTICS, 0);
    if (file == INVALID_HANDLE_VALUE) {
        return 0;
    }
    WCHAR *out = finalPathOf(file);
    CloseHandle(file);
    return out;
}

static int samePath(const WCHAR *a, const WCHAR *b)
{
    SIZE_T n = wlen(a);
    return n == wlen(b) && CompareStringOrdinal(a, (int)n, b, (int)n, 1) == CSTR_EQUAL;
}

// regFolder returns the final path of the folder that a REG_SZ or
// REG_EXPAND_SZ value of key names, or 0.
static WCHAR *regFolder(HANDLE key, const WCHAR *name)
{
    WCHAR *raw = 0, *expanded = 0, *folder = 0;
    DWORD kind = 0, size = 0;
    if (!RegQueryValueExW(key, name, 0, &kind, 0, &size) && (kind == REG_SZ || kind == REG_EXPAND_SZ) && size) {
        raw = HeapAlloc(GetProcessHeap(), HEAP_ZERO_MEMORY, (SIZE_T)size + 2 * sizeof(WCHAR));
        if (raw && !RegQueryValueExW(key, name, 0, &kind, (BYTE *)raw, &size)) {
            DWORD n = ExpandEnvironmentStringsW(raw, 0, 0);
            expanded = n ? HeapAlloc(GetProcessHeap(), HEAP_ZERO_MEMORY, ((SIZE_T)n + 1) * sizeof(WCHAR)) : 0;
            if (expanded && ExpandEnvironmentStringsW(raw, expanded, n) && expanded[0]) {
                folder = finalPath(expanded);
            }
        }
    }
    HeapFree(GetProcessHeap(), 0, raw);
    HeapFree(GetProcessHeap(), 0, expanded);
    return folder;
}

// userSid returns the SID of the account the action runs as, as a string:
// the impersonated user's, else the process's. 0 when it cannot be read.
static WCHAR *userSid(void)
{
    HANDLE token = 0;
    if (!OpenThreadToken(GetCurrentThread(), TOKEN_QUERY, 1, &token) && !OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &token)) {
        return 0;
    }
    WCHAR *sid = 0, *text = 0;
    DWORD size = 0;
    GetTokenInformation(token, 1 /* TokenUser */, 0, 0, &size);
    // TOKEN_USER starts with the pointer to the user's SID.
    void **user = size ? HeapAlloc(GetProcessHeap(), 0, size) : 0;
    if (user && GetTokenInformation(token, 1, user, size, &size) && ConvertSidToStringSidW(user[0], &text)) {
        sid = wstrdup(text, 0);
        LocalFree(text);
    }
    HeapFree(GetProcessHeap(), 0, user);
    CloseHandle(token);
    return sid;
}

// TOKEN_GROUPS: a count, then SID_AND_ATTRIBUTES {PSID; DWORD} pairs.
typedef struct {
    void *sid;
    DWORD attributes;
} tokenGroup;
typedef struct {
    DWORD count;
    tokenGroup group[1];
} tokenGroups;

// holdsAdmins reports whether groups lists the Administrators group, in
// any state: enabled, or deny-only for an administrator under UAC.
static int holdsAdmins(const tokenGroups *groups)
{
    void *admins = 0;
    int found = 0;
    if (groups && ConvertStringSidToSidW(adminsSid, &admins)) {
        for (DWORD i = 0; i < groups->count && !found; i++) {
            found = EqualSid(groups->group[i].sid, admins) != 0;
        }
        LocalFree(admins);
    }
    return found;
}

// userIsAdmin reports whether the account the action runs as is in the
// Administrators group; LocalSystem's token holds it as well.
static int userIsAdmin(void)
{
    HANDLE token = 0;
    if (!OpenThreadToken(GetCurrentThread(), TOKEN_QUERY, 1, &token) && !OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &token)) {
        return 0;
    }
    DWORD size = 0;
    GetTokenInformation(token, 2 /* TokenGroups */, 0, 0, &size);
    tokenGroups *groups = size ? HeapAlloc(GetProcessHeap(), 0, size) : 0;
    int found = groups && GetTokenInformation(token, 2, groups, size, &size) && holdsAdmins(groups);
    HeapFree(GetProcessHeap(), 0, groups);
    CloseHandle(token);
    return found;
}

// sessionIsAdmin reports, for the deferred side, whether the account that
// started the session (the engine's UserSID) is LocalSystem or in the
// Administrators group, as Authz computes its groups from the SID. The
// folder properties are secure, so for any other account its command line
// may have named the folders; LocalSystem must not remove them for it.
static int sessionIsAdmin(MSIHANDLE install)
{
    static const WCHAR userSidProp[] = u"UserSID";
    static const WCHAR systemSid[] = u"S-1-5-18";
    WCHAR *text = property(install, userSidProp);
    void *sid = 0, *rm = 0, *ctx = 0;
    tokenGroups *groups = 0;
    int found = text && wieq(text, systemSid);
    if (!found && text && ConvertStringSidToSidW(text, &sid) && AuthzInitializeResourceManager(AUTHZ_RM_FLAG_NO_AUDIT, 0, 0, 0, 0, &rm)) {
        LUID_ none = {0, 0};
        DWORD size = 0;
        if (AuthzInitializeContextFromSid(0, sid, rm, 0, none, 0, &ctx)) {
            AuthzGetInformationFromContext(ctx, AuthzContextInfoGroupsSids, 0, &size, 0);
            groups = size ? HeapAlloc(GetProcessHeap(), 0, size) : 0;
            found = groups && AuthzGetInformationFromContext(ctx, AuthzContextInfoGroupsSids, size, &size, groups) && holdsAdmins(groups);
            AuthzFreeContext(ctx);
        }
        AuthzFreeResourceManager(rm);
    }
    HeapFree(GetProcessHeap(), 0, groups);
    if (sid) {
        LocalFree(sid);
    }
    HeapFree(GetProcessHeap(), 0, text);
    return found;
}

// profileCheck returns why folder must not be removed because of the
// profile at profile (freed here), or 0: inside the installing account's
// own profile (own) only the standard folder guards apply, which name that
// account's folders; inside any other, other is the reason.
static const WCHAR *profileCheck(const WCHAR *folder, WCHAR *profile, int own, const WCHAR *other)
{
    const WCHAR *why = 0;
    if (profile && below(profile, folder)) {
        why = msgRemoveProfiles;
    } else if (profile && below(folder, profile) && !own) {
        why = other;
    }
    HeapFree(GetProcessHeap(), 0, profile);
    return why;
}

// profileRefusal returns why folder must not be removed because of the user
// profiles, or 0: the profiles folder (ProfilesDirectory, C:\Users by
// default), a folder that holds it, a folder directly in it, a folder that
// is or holds a profile of ProfileList, Public or Default, and any folder
// inside a profile other than that of own, the SID of the account that
// started the session (the engine's UserSID, which no earlier action can
// change for the deferred side, FILE-12). A per-machine removal therefore
// keeps that account's own profile open; only a session that itself runs
// as LocalSystem (a deployment tool) has no profile of its own outside the
// Windows folder, and then every user's data is refused. When the profiles
// cannot be read, everything is.
static const WCHAR *profileRefusal(const WCHAR *folder, const WCHAR *own)
{
    HANDLE key = 0;
    if (RegOpenKeyExW(HKLM_, profileListKey, 0, KEY_QUERY_VALUE | KEY_ENUMERATE_SUB_KEYS | KEY_WOW64_64KEY, &key)) {
        return msgRemoveNoProfiles;
    }
    WCHAR *root = regFolder(key, profilesDirectory);
    const WCHAR *why = root ? 0 : msgRemoveNoProfiles;
    if (root && below(root, folder)) {
        why = msgRemoveProfiles;
    } else if (root && below(folder, root)) {
        const WCHAR *rest = folder + wlen(root);
        while (*rest && *rest != '\\') {
            rest++;
        }
        if (rest[0] == '\\' && rest[1] == 0) {
            why = msgRemoveProfiles;
        }
    }
    HeapFree(GetProcessHeap(), 0, root);
    static const WCHAR *const shared[] = {u"Public", u"Default"};
    for (SIZE_T i = 0; !why && i < sizeof(shared) / sizeof(shared[0]); i++) {
        why = profileCheck(folder, regFolder(key, shared[i]), 0, msgRemoveSharedProfile);
    }
    WCHAR sid[256];
    for (DWORD i = 0; !why; i++) {
        DWORD n = sizeof(sid) / sizeof(sid[0]);
        long err = RegEnumKeyExW(key, i, sid, &n, 0, 0, 0, 0);
        if (err == ERROR_NO_MORE_ITEMS) {
            break;
        }
        if (err == ERROR_MORE_DATA) {
            // A SID is far shorter than the buffer; this is no profile.
            continue;
        }
        HANDLE sub = 0;
        if (err || RegOpenKeyExW(key, sid, 0, KEY_QUERY_VALUE | KEY_WOW64_64KEY, &sub)) {
            why = msgRemoveNoProfiles;
            break;
        }
        // Windows keeps a profile it could not load under "<SID>.bak",
        // usually with the same ProfileImagePath; that SID is still ours.
        static const WCHAR bak[] = u".bak";
        SIZE_T ownLen = own ? wlen(own) : 0;
        int mine = own && (n == ownLen || (n == ownLen + 4 && CompareStringOrdinal(sid + ownLen, 4, bak, 4, 1) == CSTR_EQUAL)) &&
                   CompareStringOrdinal(own, (int)ownLen, sid, (int)ownLen, 1) == CSTR_EQUAL;
        why = profileCheck(folder, regFolder(sub, profileImagePath), mine, msgRemoveOtherProfile);
        RegCloseKey(sub);
    }
    RegCloseKey(key);
    return why;
}

// refusal returns why a folder must not be removed, or 0. folder is the
// final path with a trailing backslash; protectedName receives the
// property name of a standard folder it reaches. Standard folders are
// compared by their final paths too, so a link to one is caught.
static const WCHAR *refusal(const removeData *rd, const WCHAR *folder, const WCHAR **protectedName)
{
    if (wlen(folder) <= 3) {
        return msgRemoveRoot;
    }
    const WCHAR *why = profileRefusal(folder, rd->own);
    for (DWORD i = 0; !why && i < rd->standards; i++) {
        // The line is cut after the name; the value follows.
        const WCHAR *name = rd->standard[i], *value = name + wlen(name) + 1;
        WCHAR *full = *value ? finalPath(value) : 0;
        const WCHAR *std = full ? full : value;
        if (wlen(std) && std[wlen(std) - 1] == '\\') {
            if (below(std, folder)) {
                why = msgRemoveStandard;
                *protectedName = name;
            } else if (wieq(name, windowsFolder) && below(folder, std)) {
                why = msgRemoveWindows;
            }
        }
        HeapFree(GetProcessHeap(), 0, full);
    }
    return why;
}

// checkFolder resolves the folder a property names and returns why it must
// not be removed, or 0 with *folder set to its path. *detail receives a
// text to log after the reason, owned by the caller when *owned is set.
static const WCHAR *checkFolder(const removeData *rd, WCHAR *path, WCHAR **folder, const WCHAR **detail, WCHAR **owned)
{
    if (!plainPath(path)) {
        return msgRemovePath;
    }
    DWORD attrs = GetFileAttributesW(path);
    if (attrs == INVALID_FILE_ATTRIBUTES || !(attrs & FILE_ATTRIBUTE_DIRECTORY)) {
        return msgRemoveMissing;
    }
    if (attrs & FILE_ATTRIBUTE_REPARSE_POINT) {
        return msgRemoveReparse;
    }
    WCHAR *full = longPath(path), *final = finalPath(path);
    const WCHAR *why = 0;
    if (!full || !final) {
        why = msgRemovePath;
    } else if (!samePath(full, final)) {
        // A junction or symbolic link above the folder, or a subst drive:
        // the guards compare paths, so only a path without one is checked.
        why = msgRemoveRedirected;
        *detail = *owned = final;
        final = 0;
    } else {
        why = refusal(rd, final, detail);
    }
    HeapFree(GetProcessHeap(), 0, full);
    if (why) {
        HeapFree(GetProcessHeap(), 0, final);
    } else {
        *folder = final;
    }
    return why;
}

// packageRefusal returns why folder (a final path) must not be removed now
// because it is or holds one of the package's own directories, whose path
// it then hands back in *owned, or 0. Such a folder goes only when the
// whole product is removed: on an install, a repair or the removal of one
// feature the installer keeps files there that the move would take away.
// The entry's property may hold the install dir under any name, so this
// compares paths, not names; a directory that does not exist yet compares
// by its path as the engine resolved it.
static const WCHAR *packageRefusal(const removeData *rd, const WCHAR *folder, WCHAR **owned)
{
    const WCHAR *why = 0;
    for (DWORD i = 0; !rd->all && !why && i < rd->packages; i++) {
        WCHAR *value = trim(rd->package[i]);
        if (!*value) {
            continue;
        }
        WCHAR *path = finalPath(value);
        if (!path) {
            path = wstrdup(value, 1);
            SIZE_T n = path ? wlen(path) : 0;
            if (n && path[n - 1] != '\\') {
                path[n] = '\\';
                path[n + 1] = 0;
            }
        }
        if (path && below(path, folder)) {
            why = msgRemovePackage;
            *owned = path;
            path = 0;
        }
        HeapFree(GetProcessHeap(), 0, path);
    }
    return why;
}

// checkEntry checks the f line of one entry, cut after the property's
// name, and returns the final path of its folder when it may be removed,
// or 0 after logging why not.
static WCHAR *checkEntry(MSIHANDLE install, const removeData *rd, WCHAR *line)
{
    WCHAR *name = line, *value = trim(line + wlen(line) + 1), *folder = 0, *owned = 0;
    const WCHAR *why = 0, *detail = 0;
    if (!*value) {
        why = msgRemoveEmpty;
    } else {
        WCHAR *path = wstrdup(value, 1);
        if (path) {
            SIZE_T n = wlen(path);
            if (n && path[n - 1] != '\\') {
                path[n] = '\\';
                path[n + 1] = 0;
            }
            why = checkFolder(rd, path, &folder, &detail, &owned);
            HeapFree(GetProcessHeap(), 0, path);
            WCHAR *pkg = 0;
            if (!why && (why = packageRefusal(rd, folder, &pkg)) != 0) {
                HeapFree(GetProcessHeap(), 0, owned);
                owned = pkg;
                detail = pkg;
                HeapFree(GetProcessHeap(), 0, folder);
                folder = 0;
            }
        } else {
            why = msgNoMemory;
        }
    }
    if (why) {
        static const WCHAR space[] = u" ";
        wbuf text = {0, 0, 0};
        const WCHAR *shown = *value ? value : name;
        if (wbufAdd(&text, why, wlen(why)) && (!detail || (wbufAdd(&text, space, 1) && wbufAdd(&text, detail, wlen(detail))))) {
            logft(install, tmplRemove, shown, text.s, 0);
        }
        HeapFree(GetProcessHeap(), 0, text.s);
        HeapFree(GetProcessHeap(), 0, owned);
        return 0;
    }
    logft(install, tmplRemove, msgRemoving, folder, 0);
    return folder;
}

// parseRemoveData splits the CustomActionData (see the comment above
// tmplRemove) into rd and returns the op, or 0 when it is malformed, after
// logging why. product receives the ProductCode, cut at its end.
static WCHAR parseRemoveData(MSIHANDLE install, WCHAR *data, removeData *rd, WCHAR **product)
{
    WCHAR op = data[0];
    if ((op != 'u' && op != 'm' && op != 'd' && op != 'c') || (data[1] != 'a' && data[1] != 'k') || wlen(data) < 41 || data[2] != '{' ||
        data[39] != '}' || data[40] != ':') {
        logft(install, tmplRemove, msgRemoveData, 0, 0);
        return 0;
    }
    for (int i = 3; i < 39; i++) {
        WCHAR c = wlower(data[i]);
        if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || c == '-')) {
            logft(install, tmplRemove, msgRemoveData, 0, 0);
            return 0;
        }
    }
    rd->all = data[1] == 'a';
    data[40] = 0;
    *product = data + 2;
    WCHAR *p = data + 41;
    DWORD count = 0, fixed = 0, lines = 0, digits = 0;
    for (; *p == '#'; p++) {
        count++;
    }
    // The first line is n and the number of fixed lines, which the builder
    // wrote into the first fixed setter.
    if (p[0] != '\n' || p[1] != 'n') {
        logft(install, tmplRemove, msgRemoveData, 0, 0);
        return 0;
    }
    WCHAR *q = p + 2;
    for (; *q >= '0' && *q <= '9' && digits < 6; q++, digits++) {
        fixed = fixed * 10 + (DWORD)(*q - '0');
    }
    if (!digits || (*q && *q != '\n')) {
        logft(install, tmplRemove, msgRemoveData, 0, 0);
        return 0;
    }
    for (; *q; q++) {
        lines += *q == '\n';
    }
    if (lines != fixed + count) {
        // A line break in a formatted folder added lines.
        logft(install, tmplRemove, msgRemoveLines, 0, 0);
        return 0;
    }
    p = p + 2 + digits;
    WCHAR **all = HeapAlloc(GetProcessHeap(), 0, (lines ? lines : 1) * sizeof(WCHAR *));
    if (!all) {
        logft(install, tmplRemove, msgNoMemory, 0, 0);
        return 0;
    }
    // The lines start after each line break; they are sorted s, p, f by
    // the builder, and each kind keeps its order.
    DWORD n = 0;
    for (WCHAR *line = *p ? p + 1 : 0; line; n++) {
        WCHAR *next = field(line, '\n');
        all[n] = line;
        line = next;
    }
    rd->standard = all;
    for (DWORD i = 0; i < n; i++) {
        WCHAR kind = all[i][0];
        int ordered = (kind == 's' && !rd->packages && !rd->count) || (kind == 'p' && !rd->count) || kind == 'f';
        WCHAR *rest = all[i] + 1;
        // s and f lines carry a name before `|`, which field cuts.
        if (!ordered || ((kind == 's' || kind == 'f') && (!field(rest, '|') || !*rest))) {
            logft(install, tmplRemove, msgRemoveData, 0, 0);
            HeapFree(GetProcessHeap(), 0, all);
            rd->standard = 0;
            return 0;
        }
        all[i] = rest;
        if (kind == 's') {
            rd->standards++;
        } else if (kind == 'p') {
            rd->packages++;
        } else {
            rd->count++;
        }
    }
    if (rd->count != count) {
        logft(install, tmplRemove, msgRemoveLines, 0, 0);
        HeapFree(GetProcessHeap(), 0, all);
        rd->standard = 0;
        return 0;
    }
    rd->package = all + rd->standards;
    rd->folders = rd->package + rd->packages;
    return op;
}

// backupPath writes X:\BuilderRemoveFolders.<token> and, with index
// nonzero, \<index> after it into a heap string, for the volume of folder.
static WCHAR *backupPath(const WCHAR *folder, const WCHAR *token, DWORD index)
{
    WCHAR *out = HeapAlloc(GetProcessHeap(), 0, (3 + wlen(backupPrefix) + wlen(token) + 16) * sizeof(WCHAR));
    if (!out) {
        return 0;
    }
    SIZE_T pos = 0;
    out[pos++] = folder[0];
    out[pos++] = ':';
    out[pos++] = '\\';
    out[pos] = 0;
    wappend(out, &pos, backupPrefix);
    wappend(out, &pos, token);
    if (index) {
        out[pos++] = '\\';
        wappendnum(out, &pos, index);
    }
    return out;
}

// openNoFollow opens a folder by path for the given access without
// following a reparse point at its end; 0 when it is not a plain folder.
static HANDLE openNoFollow(const WCHAR *path, DWORD access)
{
    HANDLE h = CreateFileW(path, access | SYNCHRONIZE_ | FILE_READ_ATTRIBUTES, FILE_SHARE_ALL, 0, OPEN_EXISTING,
                           FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT, 0);
    if (h == INVALID_HANDLE_VALUE) {
        return 0;
    }
    DWORD tag[2] = {0, 0};
    if (!GetFileInformationByHandleEx(h, FileAttributeTagInfo_, tag, sizeof tag) || !(tag[0] & FILE_ATTRIBUTE_DIRECTORY) ||
        (tag[0] & FILE_ATTRIBUTE_REPARSE_POINT)) {
        CloseHandle(h);
        return 0;
    }
    return h;
}

// renameInto moves the object of h into the folder dir as name; 0 on
// success, else the Windows error.
static DWORD renameInto(HANDLE h, HANDLE dir, const WCHAR *name)
{
    SIZE_T n = wlen(name);
    // FILE_RENAME_INFORMATION.
    struct rename {
        DWORD flags;
        HANDLE root;
        DWORD length;
        WCHAR name[1];
    };
    _Static_assert(__builtin_offsetof(struct rename, name) == (sizeof(void *) == 4 ? 12 : 20), "FILE_RENAME_INFORMATION layout");
    SIZE_T size = sizeof(struct rename) + n * sizeof(WCHAR);
    struct rename *info = HeapAlloc(GetProcessHeap(), HEAP_ZERO_MEMORY, size);
    if (!info) {
        return ERROR_NOT_ENOUGH_MEMORY;
    }
    info->root = dir;
    info->length = (DWORD)(n * sizeof(WCHAR));
    memcpy(info->name, name, n * sizeof(WCHAR));
    IO_STATUS_BLOCK io;
    long st = NtSetInformationFile(h, &io, info, (DWORD)size, FileRenameInformation_);
    HeapFree(GetProcessHeap(), 0, info);
    return st >= 0 ? 0 : RtlNtStatusToDosError(st);
}

// openRelative opens the entry name (bytes long, not terminated) of the
// folder dir without following a reparse point; 0 on failure.
static HANDLE openRelative(HANDLE dir, WCHAR *name, DWORD bytes, DWORD access)
{
    UNICODE_STRING us = {(unsigned short)bytes, (unsigned short)bytes, name};
    OBJECT_ATTRIBUTES oa = {sizeof oa, dir, &us, 0, 0, 0};
    IO_STATUS_BLOCK io;
    HANDLE h = 0;
    long st = NtCreateFile(&h, access | SYNCHRONIZE_ | FILE_READ_ATTRIBUTES, &oa, &io, 0, 0, FILE_SHARE_ALL, FILE_OPEN_,
                           FILE_OPEN_REPARSE_POINT | FILE_SYNCHRONOUS_IO_NONALERT | FILE_OPEN_FOR_BACKUP_INTENT, 0, 0);
    return st >= 0 ? h : 0;
}

// plainFolder reports whether h is a folder that is no reparse point.
static int plainFolder(HANDLE h)
{
    DWORD tag[2] = {0, 0};
    return GetFileInformationByHandleEx(h, FileAttributeTagInfo_, tag, sizeof tag) && (tag[0] & FILE_ATTRIBUTE_DIRECTORY) &&
           !(tag[0] & FILE_ATTRIBUTE_REPARSE_POINT);
}

// deleteHandle marks the object of h for deletion; it goes when h is
// closed. Since Windows 10 1809 the disposition ignores a read-only
// attribute itself, which leaves the attribute alone; before that it is
// cleared first, but not on a file with more than one name, whose other
// name may be outside the tree.
static int deleteHandle(HANDLE h, DWORD attrs)
{
    // FILE_DISPOSITION_FLAG_DELETE | FILE_DISPOSITION_FLAG_IGNORE_READONLY_ATTRIBUTE
    DWORD flags = 0x1 | 0x10;
    if (SetFileInformationByHandle(h, FileDispositionInfoEx_, &flags, sizeof flags)) {
        return 1;
    }
    struct {
        long long allocation, size;
        DWORD links;
        BYTE pending, directory;
    } standard = {0, 0, 0, 0, 0};
    if ((attrs & FILE_ATTRIBUTE_READONLY) && GetFileInformationByHandleEx(h, FileStandardInfo_, &standard, sizeof standard) &&
        standard.links <= 1) {
        struct {
            long long times[4];
            DWORD attributes;
        } basic = {{0, 0, 0, 0}, attrs & ~(DWORD)FILE_ATTRIBUTE_READONLY};
        if (!basic.attributes) {
            basic.attributes = FILE_ATTRIBUTE_NORMAL;
        }
        SetFileInformationByHandle(h, FileBasicInfo_, &basic, sizeof basic);
    }
    BYTE dispose = 1;
    return SetFileInformationByHandle(h, FileDispositionInfo_, &dispose, sizeof dispose) != 0;
}

// FILE_FULL_DIR_INFO up to the name.
typedef struct {
    DWORD NextEntryOffset, FileIndex;
    long long CreationTime, LastAccessTime, LastWriteTime, ChangeTime, EndOfFile, AllocationSize;
    DWORD FileAttributes, FileNameLength, EaSize;
    WCHAR FileName[1];
} DIRINFO;
_Static_assert(__builtin_offsetof(DIRINFO, FileName) == 68, "FILE_FULL_DIR_INFO layout");

enum { DIR_BUFFER = 64 * 1024, MAX_PASSES = 4096 };

// emptyFolder deletes everything in the folder of dir: each entry is opened
// relative to dir, a junction or link as itself, so nothing outside the
// folder is reached whatever is swapped in meanwhile. It returns 0 when
// something stays.
static int emptyFolder(HANDLE dir, int depth)
{
    BYTE *buf = depth < 256 ? HeapAlloc(GetProcessHeap(), 0, DIR_BUFFER) : 0;
    if (!buf) {
        return 0;
    }
    // Each pass reads the first batch afresh, since deleting changes the
    // listing. The folder is empty when a pass sees nothing; a pass that
    // deletes nothing, or too many passes for someone still adding
    // entries, ends it.
    int empty = 0;
    for (int pass = 0, progress = 1; progress && pass < MAX_PASSES; pass++) {
        progress = 0;
        if (!GetFileInformationByHandleEx(dir, FileFullDirectoryRestartInfo_, buf, DIR_BUFFER)) {
            empty = GetLastError() == ERROR_NO_MORE_FILES;
            break;
        }
        int seen = 0;
        for (DIRINFO *e = (DIRINFO *)buf;; e = (DIRINFO *)((BYTE *)e + e->NextEntryOffset)) {
            DWORD n = e->FileNameLength / sizeof(WCHAR);
            int dot = e->FileName[0] == '.' && (n == 1 || (n == 2 && e->FileName[1] == '.'));
            if (!dot) {
                seen = 1;
                int isDir = (e->FileAttributes & FILE_ATTRIBUTE_DIRECTORY) && !(e->FileAttributes & FILE_ATTRIBUTE_REPARSE_POINT);
                HANDLE h = openRelative(dir, e->FileName, e->FileNameLength, DELETE_ | FILE_WRITE_ATTRIBUTES | (isDir ? FILE_LIST_DIRECTORY : 0));
                DWORD tag[2] = {0, 0};
                if (h && GetFileInformationByHandleEx(h, FileAttributeTagInfo_, tag, sizeof tag)) {
                    int nowDir = (tag[0] & FILE_ATTRIBUTE_DIRECTORY) && !(tag[0] & FILE_ATTRIBUTE_REPARSE_POINT);
                    // An entry that changed between file, link and folder
                    // since it was listed is left to the next pass, which
                    // lists it afresh.
                    if (nowDir == isDir && (!isDir || emptyFolder(h, depth + 1)) && deleteHandle(h, tag[0])) {
                        progress = 1;
                    }
                }
                if (h) {
                    CloseHandle(h);
                }
            }
            if (!e->NextEntryOffset) {
                break;
            }
        }
        if (!seen) {
            empty = 1;
            break;
        }
    }
    HeapFree(GetProcessHeap(), 0, buf);
    return empty;
}

// deleteFolder deletes the folder of h with everything in it.
static int deleteFolder(HANDLE h)
{
    DWORD tag[2] = {0, 0};
    return emptyFolder(h, 0) && GetFileInformationByHandleEx(h, FileAttributeTagInfo_, tag, sizeof tag) && deleteHandle(h, tag[0]);
}

// sidIs reports whether sid is the SID in text form.
static int sidIs(void *sid, const WCHAR *text)
{
    WCHAR *s = 0;
    int same = text && ConvertSidToStringSidW(sid, &s) && samePath(s, text);
    if (s) {
        LocalFree(s);
    }
    return same;
}

// openBackup opens the backup folder at root for access, and only when it
// is the folder this removal created: a plain folder at that very path,
// owned by LocalSystem, the administrators or the account the action runs
// as, with a protected access list. A folder or junction someone else put
// there with the token from a log is refused. 0 otherwise.
static HANDLE openBackup(const WCHAR *root, DWORD access)
{
    HANDLE h = openNoFollow(root, access | READ_CONTROL_);
    if (!h) {
        return 0;
    }
    WCHAR *final = finalPathOf(h), *user = userSid();
    void *owner = 0, *sd = 0;
    ACL *dacl = 0;
    unsigned short control = 0;
    DWORD revision = 0;
    SIZE_T n = wlen(root);
    int ok = final && wlen(final) == n + 1 && CompareStringOrdinal(final, (int)n, root, (int)n, 1) == CSTR_EQUAL &&
             !GetSecurityInfo(h, SE_FILE_OBJECT, OWNER_SECURITY_INFORMATION | DACL_SECURITY_INFORMATION, &owner, 0, &dacl, 0, &sd) &&
             GetSecurityDescriptorControl(sd, &control, &revision) && (control & SE_DACL_PROTECTED) &&
             (sidIs(owner, u"S-1-5-18") || sidIs(owner, u"S-1-5-32-544") || sidIs(owner, user));
    if (sd) {
        LocalFree(sd);
    }
    HeapFree(GetProcessHeap(), 0, user);
    HeapFree(GetProcessHeap(), 0, final);
    if (!ok) {
        CloseHandle(h);
        return 0;
    }
    return h;
}

// openMoved opens backup folder <index> relative to root.
static HANDLE openMoved(HANDLE root, DWORD index, DWORD access)
{
    WCHAR num[12];
    SIZE_T pos = 0;
    wappendnum(num, &pos, index);
    HANDLE h = openRelative(root, num, (DWORD)(pos * sizeof(WCHAR)), access);
    if (h && !plainFolder(h)) {
        CloseHandle(h);
        h = 0;
    }
    return h;
}

// moveAside moves folder, opened without following a reparse point and
// checked to still be the folder at that path, into the backup folder as
// <index>. The backup folder is created on the first call for its volume
// (created holds a bit per drive letter), and only a folder this removal
// created is used: one that existed before could carry any owner and
// access list.
static int moveAside(MSIHANDLE install, const WCHAR *folder, const WCHAR *token, DWORD index, DWORD *created)
{
    WCHAR *root = backupPath(folder, token, 0), *final = 0;
    DWORD volume = 1u << (wlower(folder[0]) - 'a');
    HANDLE dir = 0, h = 0;
    int ok = 0;
    if (!root) {
        logft(install, tmplRemove, msgNoMemory, 0, 0);
        return 0;
    }
    int fresh = 0;
    if (!(*created & volume)) {
        // A new folder of the token's own: only LocalSystem, the
        // administrators and its owner may open it.
        void *sd = 0;
        if (ConvertStringSecurityDescriptorToSecurityDescriptorW(backupSddl, 1, &sd, 0)) {
            SECURITY_ATTRIBUTES sa = {sizeof sa, sd, 0};
            if (CreateDirectoryW(root, &sa)) {
                *created |= volume;
                fresh = 1;
            }
            LocalFree(sd);
        }
    }
    dir = *created & volume ? openBackup(root, FILE_ADD_SUBDIRECTORY) : 0;
    if (!dir && fresh) {
        // A volume without access lists (FAT, exFAT) keeps no protected
        // DACL, so openBackup refuses the folder just made; take it away
        // again (a junction put there instead goes as a link) and leave
        // the folders of this volume in place.
        RemoveDirectoryW(root);
        *created &= ~volume;
    }
    h = dir ? openNoFollow(folder, DELETE_) : 0;
    final = h ? finalPathOf(h) : 0;
    if (!dir) {
        logft(install, tmplRemove, folder, msgRemoveBackup, GetLastError());
    } else if (!h) {
        // Gone since the check, or a reparse point now.
        if (GetFileAttributesW(folder) != INVALID_FILE_ATTRIBUTES) {
            logft(install, tmplRemove, folder, msgRemoveChanged, 0);
        }
    } else if (!final || !samePath(final, folder)) {
        logft(install, tmplRemove, folder, msgRemoveChanged, 0);
    } else {
        WCHAR num[12];
        SIZE_T pos = 0;
        wappendnum(num, &pos, index);
        DWORD err = renameInto(h, dir, num);
        if (!err) {
            logft(install, tmplRemove, folder, msgRemoveMoved, 0);
            ok = 1;
        } else if (err == ERROR_NOT_SAME_DEVICE) {
            // The backup folder is on the drive letter's volume, not on a
            // volume mounted in a folder above this one.
            logft(install, tmplRemove, folder, msgRemoveVolume, 0);
        } else {
            logft(install, tmplRemove, folder, msgRemoveBusy, err);
        }
    }
    HeapFree(GetProcessHeap(), 0, final);
    if (h) {
        CloseHandle(h);
    }
    if (dir) {
        CloseHandle(dir);
    }
    HeapFree(GetProcessHeap(), 0, root);
    return ok;
}

// restore moves backup folder <index> back to folder (with its trailing
// backslash): by handle into the parent folder, opened without following a
// reparse point and checked to still be at that path, so that a junction
// put in place of a folder above it cannot send the tree elsewhere.
// It returns 1 when restored, 0 when nothing was moved for index (the
// folder was left in place, or a second entry named it) and -1 on failure.
static int restore(HANDLE root, DWORD index, WCHAR *folder)
{
    SIZE_T n = wlen(folder) - 1, cut = n;
    while (cut && folder[cut - 1] != '\\') {
        cut--;
    }
    HANDLE moved = openMoved(root, index, DELETE_), parent = 0;
    WCHAR *final = 0;
    int ok = moved ? -1 : 0;
    if (moved && cut >= 3) {
        WCHAR keep = folder[cut];
        folder[cut] = 0;
        parent = openNoFollow(folder, FILE_ADD_SUBDIRECTORY);
        final = parent ? finalPathOf(parent) : 0;
        int same = final && samePath(final, folder);
        folder[cut] = keep;
        folder[n] = 0;
        ok = same && !renameInto(moved, parent, folder + cut) ? 1 : -1;
        folder[n] = '\\';
    }
    HeapFree(GetProcessHeap(), 0, final);
    if (parent) {
        CloseHandle(parent);
    }
    if (moved) {
        CloseHandle(moved);
    }
    return ok;
}

// regText returns a REG_SZ value of key as a heap string, or 0.
static WCHAR *regText(HANDLE key, const WCHAR *name)
{
    DWORD kind = 0, size = 0;
    WCHAR *out = 0;
    if (!RegQueryValueExW(key, name, 0, &kind, 0, &size) && kind == REG_SZ && size) {
        out = HeapAlloc(GetProcessHeap(), HEAP_ZERO_MEMORY, (SIZE_T)size + 2 * sizeof(WCHAR));
        if (out && (RegQueryValueExW(key, name, 0, &kind, (BYTE *)out, &size) || kind != REG_SZ)) {
            HeapFree(GetProcessHeap(), 0, out);
            out = 0;
        }
    }
    return out;
}

// regSetText writes a REG_SZ value; 0 on failure.
static int regSetText(HANDLE key, const WCHAR *name, const WCHAR *text)
{
    return !RegSetValueExW(key, name, 0, REG_SZ, (const BYTE *)text, (DWORD)((wlen(text) + 1) * sizeof(WCHAR)));
}

// newToken writes 32 random hexadecimal digits into token; 0 on failure.
static int newToken(WCHAR token[33])
{
    BYTE random[16];
    SIZE_T pos = 0;
    token[0] = 0;
    if (!SystemFunction036(random, sizeof random)) {
        return 0;
    }
    for (SIZE_T i = 0; i < sizeof random; i++) {
        wappendhex(token, &pos, random[i]);
    }
    return 1;
}

// keepLeft is called when backup folder <index> of token cannot be put
// back or deleted: it logs where the folder stays and keeps the original
// path, which only the record ties to the backup folder's random name,
// under HKCU\Software\msi-builder\RemoveFoldersLeft\<token> as value
// <index>, with the product code. Nothing deletes that key; it is there
// for whoever cleans up by hand.
static void keepLeft(MSIHANDLE install, const WCHAR *product, const WCHAR *token, DWORD index, const WCHAR *folder, const WCHAR *why, DWORD code)
{
    static const WCHAR space[] = u" ", value[] = u", value ";
    WCHAR *backup = backupPath(folder, token, index);
    wbuf text = {0, 0, 0}, keyPath = {0, 0, 0};
    if (!backup || !wbufAdd(&text, why, wlen(why)) || !wbufAdd(&text, space, 1) || !wbufAdd(&text, backup, wlen(backup))) {
        logft(install, tmplRemove, folder, why, code);
    } else {
        logft(install, tmplRemove, folder, text.s, code);
    }
    WCHAR name[12];
    SIZE_T pos = 0;
    wappendnum(name, &pos, index);
    HANDLE key = 0;
    int kept = wbufAdd(&keyPath, removeLeftKey, wlen(removeLeftKey)) && wbufAdd(&keyPath, token, wlen(token)) &&
               !RegCreateKeyExW(HKCU_, keyPath.s, 0, 0, 0, KEY_SET_VALUE, 0, &key, 0) && regSetText(key, name, folder) &&
               (!product || regSetText(key, removeLeftProduct, product));
    if (key) {
        RegCloseKey(key);
    }
    text.len = 0;
    if (kept && backup && wbufAdd(&text, msgRemoveKept, wlen(msgRemoveKept)) && wbufAdd(&text, space, 1) &&
        wbufAdd(&text, keyPath.s, keyPath.len) && wbufAdd(&text, value, wlen(value)) && wbufAdd(&text, name, pos)) {
        logft(install, tmplRemove, backup, text.s, 0);
    } else if (!kept) {
        logft(install, tmplRemove, backup ? backup : folder, msgRemoveNotKept, 0);
    }
    HeapFree(GetProcessHeap(), 0, text.s);
    HeapFree(GetProcessHeap(), 0, keyPath.s);
    HeapFree(GetProcessHeap(), 0, backup);
}

// recordToken reads the token of a record into token; 0 when it has none
// that is 32 lowercase hexadecimal digits.
static int recordToken(HANDLE key, WCHAR token[33])
{
    WCHAR *recorded = regText(key, removeRecordToken);
    int ok = recorded && wlen(recorded) == 32;
    for (int i = 0; ok && i < 32; i++) {
        WCHAR c = recorded[i];
        ok = (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f');
    }
    if (ok) {
        memcpy(token, recorded, 33 * sizeof(WCHAR));
    }
    HeapFree(GetProcessHeap(), 0, recorded);
    return ok;
}

// recordedFolder reads value name of a record: a folder path with its
// trailing backslash, or 0.
static WCHAR *recordedFolder(HANDLE key, const WCHAR *name)
{
    WCHAR *folder = regText(key, name);
    SIZE_T n = folder ? wlen(folder) : 0;
    if (!n || folder[n - 1] != '\\' || !plainPath(folder)) {
        HeapFree(GetProcessHeap(), 0, folder);
        return 0;
    }
    return folder;
}

// keepStale is called before a new removal replaces a record that reached
// neither its rollback nor its commit (the installer process ended in
// between): every recorded folder whose backup folder is still there is
// kept with keepLeft, so its original path is not lost with the record.
static void keepStale(MSIHANDLE install, const WCHAR *product, const WCHAR *recordPath)
{
    HANDLE key = 0;
    WCHAR token[33];
    if (RegOpenKeyExW(HKCU_, recordPath, 0, KEY_QUERY_VALUE, &key)) {
        return;
    }
    if (!recordToken(key, token)) {
        RegCloseKey(key);
        return;
    }
    for (DWORD i = 0;; i++) {
        WCHAR name[16];
        DWORD n = 16, index = 0;
        long err = RegEnumValueW(key, i, name, &n, 0, 0, 0, 0);
        if (err == ERROR_MORE_DATA) {
            continue; // a longer name is no folder number
        }
        if (err) {
            break;
        }
        int number = n > 0 && n < 10;
        for (DWORD j = 0; number && j < n; j++) {
            number = name[j] >= '0' && name[j] <= '9';
            index = index * 10 + (DWORD)(name[j] - '0');
        }
        WCHAR *folder = number && index ? recordedFolder(key, name) : 0;
        WCHAR *rootPath = folder ? backupPath(folder, token, 0) : 0;
        HANDLE root = rootPath ? openBackup(rootPath, FILE_LIST_DIRECTORY) : 0;
        HANDLE moved = root ? openMoved(root, index, FILE_LIST_DIRECTORY) : 0;
        if (moved) {
            keepLeft(install, product, token, index, folder, msgRemoveStale, 0);
            CloseHandle(moved);
        }
        if (root) {
            CloseHandle(root);
        }
        HeapFree(GetProcessHeap(), 0, rootPath);
        HeapFree(GetProcessHeap(), 0, folder);
    }
    RegCloseKey(key);
}

// moveFolders is the deferred side: it checks each f line's folder and
// moves the accepted ones aside as <n>, n the number of the line, then with
// op d deletes them at once. With op m it first records the token and,
// before each move, the folder's final path under the record key, so that
// the rollback and the commit find what moved; without a record nothing
// moves. It returns the drive letters it used as bits.
static DWORD moveFolders(MSIHANDLE install, const removeData *rd, WCHAR op, const WCHAR *recordPath, WCHAR token[33])
{
    HANDLE key = 0;
    DWORD created = 0, volumes = 0;
    const WCHAR *product = recordPath + wlen(removeRecordKey);
    if (op == 'm') {
        // A record left by a removal that reached neither its rollback nor
        // its commit is stale; its backup folders stay where they are, and
        // what they hold is kept apart before the record goes.
        keepStale(install, product, recordPath);
        RegDeleteTreeW(HKCU_, recordPath);
        if (RegCreateKeyExW(HKCU_, recordPath, 0, 0, 0, KEY_SET_VALUE, 0, &key, 0)) {
            key = 0;
        }
    }
    if (!newToken(token) || (op == 'm' && (!key || !regSetText(key, removeRecordToken, token)))) {
        logft(install, tmplRemove, op == 'm' ? msgRemoveRecord : msgRemoveData, 0, GetLastError());
        if (key) {
            RegCloseKey(key);
        }
        return 0;
    }
    for (DWORD i = 0; i < rd->count; i++) {
        WCHAR *folder = checkEntry(install, rd, rd->folders[i]);
        WCHAR name[12];
        SIZE_T pos = 0;
        wappendnum(name, &pos, i + 1);
        if (!folder) {
            continue;
        }
        volumes |= 1u << (wlower(folder[0]) - 'a');
        if (op == 'm' && !regSetText(key, name, folder)) {
            logft(install, tmplRemove, folder, msgRemoveRecord, GetLastError());
        } else if (moveAside(install, folder, token, i + 1, &created) && op == 'd') {
            WCHAR *rootPath = backupPath(folder, token, 0);
            HANDLE root = rootPath ? openBackup(rootPath, DELETE_ | FILE_LIST_DIRECTORY | FILE_WRITE_ATTRIBUTES) : 0;
            HANDLE moved = root ? openMoved(root, i + 1, DELETE_ | FILE_LIST_DIRECTORY | FILE_WRITE_ATTRIBUTES) : 0;
            if (moved && deleteFolder(moved)) {
                logft(install, tmplRemove, msgRemoveDeleted, folder, 0);
            } else {
                keepLeft(install, product, token, i + 1, folder, msgRemoveLeft, 0);
            }
            if (moved) {
                CloseHandle(moved);
            }
            if (root) {
                CloseHandle(root);
            }
            HeapFree(GetProcessHeap(), 0, rootPath);
        }
        HeapFree(GetProcessHeap(), 0, folder);
    }
    if (key) {
        RegCloseKey(key);
    }
    return volumes;
}

// finishFolders is the rollback (u) and the commit (c) side: for every
// folder the deferred action recorded, the rollback, in reverse order so
// that a folder moved after one inside it is back before that one returns
// into it, moves it back, and the commit deletes it. It returns the drive
// letters it used as bits and the token in token.
static DWORD finishFolders(MSIHANDLE install, const removeData *rd, WCHAR op, const WCHAR *recordPath, WCHAR token[33])
{
    HANDLE key = 0;
    DWORD volumes = 0;
    const WCHAR *product = recordPath + wlen(removeRecordKey);
    if (RegOpenKeyExW(HKCU_, recordPath, 0, KEY_QUERY_VALUE, &key)) {
        return 0; // nothing was moved
    }
    int ok = recordToken(key, token);
    for (DWORD i = 0; ok && i < rd->count; i++) {
        DWORD index = op == 'u' ? rd->count - i : i + 1;
        WCHAR name[12];
        SIZE_T pos = 0;
        wappendnum(name, &pos, index);
        WCHAR *folder = recordedFolder(key, name);
        if (!folder) {
            continue;
        }
        volumes |= 1u << (wlower(folder[0]) - 'a');
        WCHAR *rootPath = backupPath(folder, token, 0);
        HANDLE root = rootPath ? openBackup(rootPath, DELETE_ | FILE_LIST_DIRECTORY | FILE_WRITE_ATTRIBUTES) : 0;
        HANDLE moved = root && op == 'c' ? openMoved(root, index, DELETE_ | FILE_LIST_DIRECTORY | FILE_WRITE_ATTRIBUTES) : 0;
        int restored = op == 'u' && root ? restore(root, index, folder) : 0;
        if (restored > 0) {
            logft(install, tmplRemove, folder, msgRemoveRestored, 0);
        } else if (restored < 0) {
            // The record goes below; what ties the backup folder to the
            // folder is kept apart.
            keepLeft(install, product, token, index, folder, msgRemoveRestore, GetLastError());
        } else if (moved && deleteFolder(moved)) {
            logft(install, tmplRemove, msgRemoveDeleted, folder, 0);
        } else if (moved) {
            keepLeft(install, product, token, index, folder, msgRemoveLeft, 0);
        }
        if (moved) {
            CloseHandle(moved);
        }
        if (root) {
            CloseHandle(root);
        }
        HeapFree(GetProcessHeap(), 0, rootPath);
        HeapFree(GetProcessHeap(), 0, folder);
    }
    RegCloseKey(key);
    RegDeleteTreeW(HKCU_, recordPath);
    return volumes;
}

// The deferred, rollback and commit side; see the comment above
// tmplRemove.
__declspec(dllexport) UINT WINAPI BuilderRemoveFoldersExec(MSIHANDLE install)
{
    static const WCHAR customActionData[] = u"CustomActionData";
    static const WCHAR userSidProp[] = u"UserSID";
    WCHAR *data = property(install, customActionData), *product = 0;
    removeData rd = {0, 0, 0, 0, 0, 0, 0, 0};
    WCHAR op = data ? parseRemoveData(install, data, &rd, &product) : 0;
    if (!data) {
        logft(install, tmplRemove, msgRemoveData, 0, 0);
    }
    // Every op checks: an account that is no administrator must not steer
    // LocalSystem with the folder properties, which are secure, so its
    // command line sets them. A deferred action that runs with the user's
    // own rights (a per-user install without elevation) can do nothing the
    // user could not, so it skips the check.
    if (op && userIsAdmin() && !sessionIsAdmin(install)) {
        logft(install, tmplRemove, msgRemoveNotAdminExec, 0, 0);
        op = 0;
    }
    wbuf recordPath = {0, 0, 0};
    if (op && (!wbufAdd(&recordPath, removeRecordKey, wlen(removeRecordKey)) || !wbufAdd(&recordPath, product, wlen(product)))) {
        logft(install, tmplRemove, msgNoMemory, 0, 0);
        op = 0;
    }
    rd.own = op ? property(install, userSidProp) : 0;
    WCHAR token[33] = {0};
    DWORD volumes = 0;
    if (op == 'm' || op == 'd') {
        volumes = moveFolders(install, &rd, op, recordPath.s, token);
    } else if (op) {
        volumes = finishFolders(install, &rd, op, recordPath.s, token);
    }
    // Each backup folder goes once it is empty.
    for (WCHAR drive = 'a'; op != 'm' && drive <= 'z'; drive++) {
        WCHAR *rootPath = volumes & (1u << (drive - 'a')) ? backupPath(&drive, token, 0) : 0;
        HANDLE root = rootPath ? openBackup(rootPath, DELETE_) : 0;
        if (root) {
            deleteHandle(root, 0);
            CloseHandle(root);
        }
        HeapFree(GetProcessHeap(), 0, rootPath);
    }
    HeapFree(GetProcessHeap(), 0, rd.own);
    HeapFree(GetProcessHeap(), 0, rd.standard);
    HeapFree(GetProcessHeap(), 0, recordPath.s);
    HeapFree(GetProcessHeap(), 0, data);
    return ERROR_SUCCESS;
}

// Closing running copies of package files (spec 17.9). BuilderCloseSchedule,
// an immediate action before InstallValidate, reads the property
// BuilderCloseApps that the builder writes, one entry per line:
//
//     <condition> US <timeout>| US <path>
//
// (US is U+001F; timeout is milliseconds in hexadecimal). For every entry
// whose condition is true it formats the path, finds the processes whose
// image is that file, posts WM_CLOSE to their windows and waits up to the
// timeout for them to exit. It runs as the installing user, so it can reach
// only the windows on the desktop of its custom action server and only
// processes the user may open. The deferred action BuilderCloseExec (entry
// point BuilderClose) runs as LocalSystem after StopServices and before
// RemoveFiles and ends every process still running from one of the paths in
// its CustomActionData, `|`-separated, in any session. That list is built by
// type-51 actions of the engine, a reset to `|` and then one per file that
// terminates, not by the schedule action: the impersonated custom action server runs with the
// user's token, so what it hands to LocalSystem must not decide which
// processes are ended. A process matches when its image is the file: by
// the full image path, compared without regard to case, or else by the
// file's volume and file ID, since a process keeps the 8.3 path it was
// started by. A copy of the program in another folder is
// left alone. Neither action fails the install: a file that is still in use
// goes through the engine's files-in-use handling as before. Log lines read
// `BuilderClose: ...`.

static const WCHAR tmplClose[] = u"BuilderClose: [1]";
static const WCHAR propCloseApps[] = u"BuilderCloseApps";
static const WCHAR msgCloseData[] = u"malformed close data";
static const WCHAR msgSnapshot[] = u"cannot list the running processes";
static const WCHAR msgAsked[] = u"asked to close";
static const WCHAR msgNoWindow[] = u"has no window to close on this desktop";
static const WCHAR msgExited[] = u"exited";
static const WCHAR msgStillRunning[] = u"still running after the timeout";
static const WCHAR msgEnded[] = u"ended";
static const WCHAR msgGone[] = u"exited before it was ended";
static const WCHAR msgNotGone[] = u"was ended but has not exited yet";
static const WCHAR msgEndFailed[] = u"cannot end";
static const WCHAR msgCannotOpen[] = u"cannot open the process of";
static const WCHAR msgNoneRunning[] = u"not running";
static const WCHAR msgClosePid[] = u"process ";

// PROCESSENTRY32W as tlhelp32.h lays it out: 556 bytes on x86, 568 on x64
// and arm64.
typedef struct {
    DWORD dwSize;
    DWORD cntUsage;
    DWORD th32ProcessID;
    SIZE_T th32DefaultHeapID;
    DWORD th32ModuleID;
    DWORD cntThreads;
    DWORD th32ParentProcessID;
    long pcPriClassBase;
    DWORD dwFlags;
    WCHAR szExeFile[260];
} PROCESSENTRY32W;

_Static_assert(sizeof(PROCESSENTRY32W) == (sizeof(void *) == 4 ? 556 : 568), "PROCESSENTRY32W layout");

typedef void *HWND;
typedef BOOL(WINAPI *WNDENUMPROC)(HWND, INTPTR);

// kernel32.dll
HANDLE WINAPI CreateToolhelp32Snapshot(DWORD, DWORD);
BOOL WINAPI Process32FirstW(HANDLE, PROCESSENTRY32W *);
BOOL WINAPI Process32NextW(HANDLE, PROCESSENTRY32W *);
HANDLE WINAPI OpenProcess(DWORD, BOOL, DWORD);
BOOL WINAPI QueryFullProcessImageNameW(HANDLE, DWORD, WCHAR *, DWORD *);
DWORD WINAPI GetCurrentProcessId(void);

// FILE_ID_INFO: the volume serial number and the 128-bit file ID.
typedef struct {
    unsigned long long volume;
    BYTE id[16];
} FILE_ID_INFO_;

_Static_assert(sizeof(FILE_ID_INFO_) == 24, "FILE_ID_INFO layout");

// user32.dll
BOOL WINAPI EnumWindows(WNDENUMPROC, INTPTR);
DWORD WINAPI GetWindowThreadProcessId(HWND, DWORD *);
BOOL WINAPI PostMessageW(HWND, UINT, INTPTR, INTPTR);
int WINAPI GetClassNameW(HWND, WCHAR *, int);
BOOL WINAPI IsWindowVisible(HWND);
HWND WINAPI GetWindow(HWND, UINT);

enum {
    TH32CS_SNAPPROCESS = 2,
    PROCESS_TERMINATE = 0x1,
    PROCESS_QUERY_LIMITED_INFORMATION = 0x1000,
    SYNCHRONIZE = 0x100000,
    WM_CLOSE = 0x10,
    GW_OWNER = 4,
    // The longest path QueryFullProcessImageNameW returns, in characters.
    IMAGE_PATH = 32768,
    // How long the deferred action waits for an ended process to go away.
    TERMINATE_WAIT_MS = 10000,
    // Processes of one file handled in one pass. The schedule action asks
    // the first CLOSE_MAX copies only; the deferred action takes further
    // passes, up to CLOSE_PASSES, until it finds fewer or a pass ends none.
    CLOSE_MAX = 64,
    CLOSE_PASSES = 16,
};

// closeTarget is one running copy of a file.
typedef struct {
    HANDLE process;
    DWORD pid;
    DWORD windows; // windows asked to close
    DWORD visible; // of them, visible ones without an owner
} closeTarget;

static int wieqi(const WCHAR *a, const WCHAR *b)
{
    return CompareStringOrdinal(a, -1, b, -1, 1) == CSTR_EQUAL;
}

// baseName returns the text after the last backslash.
static const WCHAR *baseName(const WCHAR *path)
{
    const WCHAR *name = path;
    for (; *path; path++) {
        if (*path == '\\') {
            name = path + 1;
        }
    }
    return name;
}

// fileId reads the volume serial number and the file ID of an existing
// file, which name it whatever path reaches it: a short name, a junction or
// a symbolic link on the way, another hard link. It returns 0 when the file
// cannot be opened.
static int fileId(const WCHAR *path, FILE_ID_INFO_ *id)
{
    HANDLE h = CreateFileW(path, FILE_READ_ATTRIBUTES, FILE_SHARE_ALL, 0, OPEN_EXISTING, FILE_FLAG_BACKUP_SEMANTICS, 0);
    if (h == INVALID_HANDLE_VALUE) {
        return 0;
    }
    int ok = GetFileInformationByHandleEx(h, FileIdInfo_, id, sizeof *id);
    CloseHandle(h);
    return ok;
}

// sameFile reports whether image, the image path of a process, is the file
// path names: by the text first, then by the file's identity, because a
// process started by the short form keeps it (CLOSE-02; one started
// through a junction reported the resolved path there), and another link
// to the file may name it otherwise (have is 0 when the file has no
// identity to compare).
static int sameFile(const WCHAR *image, const WCHAR *path, const FILE_ID_INFO_ *want, int have)
{
    if (wieqi(image, path)) {
        return 1;
    }
    // A program on a network share is not opened for a local file: an
    // unreachable share would stall the action. A junction cannot lead to
    // one.
    if (image[0] == '\\' && image[1] == '\\' && image[2] != '?' && image[2] != '.' && !(path[0] == '\\' && path[1] == '\\')) {
        return 0;
    }
    FILE_ID_INFO_ got;
    if (!have || !fileId(image, &got) || got.volume != want->volume) {
        return 0;
    }
    for (int i = 0; i < 16; i++) {
        if (got.id[i] != want->id[i]) {
            return 0;
        }
    }
    return 1;
}

// findRunning opens every process whose image is the file at path with the
// given access and returns how many it put into targets. image is a
// scratch buffer of IMAGE_PATH characters. Every process is looked at, not
// only those with the file's name, since a process started by the short
// name carries that one; each is opened for its image path alone first, so
// that only a match is opened with access.
static DWORD findRunning(MSIHANDLE install, const WCHAR *path, DWORD access, closeTarget *targets, WCHAR *image)
{
    HANDLE snap = CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0);
    if (snap == INVALID_HANDLE_VALUE) {
        logft(install, tmplClose, msgSnapshot, 0, GetLastError());
        return 0;
    }
    const WCHAR *name = baseName(path);
    FILE_ID_INFO_ want;
    int have = fileId(path, &want);
    DWORD n = 0, self = GetCurrentProcessId();
    PROCESSENTRY32W entry;
    memset(&entry, 0, sizeof entry);
    entry.dwSize = sizeof entry;
    for (BOOL more = Process32FirstW(snap, &entry); more && n < CLOSE_MAX; more = Process32NextW(snap, &entry)) {
        entry.szExeFile[259] = 0;
        if (entry.th32ProcessID == 0 || entry.th32ProcessID == self) {
            continue;
        }
        HANDLE query = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, entry.th32ProcessID);
        if (!query) {
            // Another user's program, from the immediate action, or one
            // that exited in between: the deferred action ends the first.
            // Only a process with the file's name is worth a line.
            if (wieqi(entry.szExeFile, name)) {
                logft(install, tmplClose, msgCannotOpen, entry.szExeFile, GetLastError());
            }
            continue;
        }
        DWORD size = IMAGE_PATH;
        if (!QueryFullProcessImageNameW(query, 0, image, &size) || !sameFile(image, path, &want, have)) {
            CloseHandle(query);
            continue;
        }
        // The open handle keeps the process ID from being reused.
        HANDLE process = OpenProcess(access | PROCESS_QUERY_LIMITED_INFORMATION | SYNCHRONIZE, 0, entry.th32ProcessID);
        DWORD err = GetLastError();
        CloseHandle(query);
        if (!process) {
            logft(install, tmplClose, msgCannotOpen, image, err);
            continue;
        }
        targets[n].process = process;
        targets[n].pid = entry.th32ProcessID;
        targets[n].windows = 0;
        targets[n].visible = 0;
        n++;
    }
    CloseHandle(snap);
    return n;
}

static void logPid(MSIHANDLE install, const WCHAR *what, DWORD pid, const WCHAR *path, DWORD code)
{
    static const WCHAR open[] = u" (", close[] = u")";
    WCHAR *s = HeapAlloc(GetProcessHeap(), 0, (wlen(path) + 32) * sizeof(WCHAR));
    if (s) {
        SIZE_T pos = 0;
        wappend(s, &pos, msgClosePid);
        wappendnum(s, &pos, pid);
        wappend(s, &pos, open);
        wappend(s, &pos, path);
        wappend(s, &pos, close);
        logft(install, tmplClose, s, what, code);
        HeapFree(GetProcessHeap(), 0, s);
    }
}

typedef struct {
    closeTarget *targets;
    DWORD n;
    // hidden is 0 in the first pass, which posts to visible windows that no
    // other window owns, and 1 in the second, which posts to the hidden
    // unowned windows of targets that have no such window, as a program that only
    // sits in the notification area has.
    int hidden;
} closeWindows;

// postClose is the EnumWindows callback: it posts WM_CLOSE to a top-level
// window of a target process. The input method windows that the system
// gives every GUI thread are left alone, and so is a console window or the
// hidden window of a pseudo console, which the console host may report as
// the program's: closing it ends every process attached to the console, not
// only this one.
static BOOL WINAPI postClose(HWND window, INTPTR param)
{
    static const WCHAR ime[] = u"IME", msctf[] = u"MSCTFIME UI", console[] = u"ConsoleWindowClass",
                       pseudo[] = u"PseudoConsoleWindow";
    closeWindows *w = (closeWindows *)param;
    DWORD pid = 0;
    GetWindowThreadProcessId(window, &pid);
    for (DWORD i = 0; i < w->n; i++) {
        if (w->targets[i].pid != pid) {
            continue;
        }
        if ((w->hidden ? w->targets[i].visible != 0 || IsWindowVisible(window) : !IsWindowVisible(window)) || GetWindow(window, GW_OWNER)) {
            break;
        }
        WCHAR cls[24];
        int len = GetClassNameW(window, cls, 24);
        if (len > 0 && (wieqi(cls, ime) || wieqi(cls, msctf) || wieqi(cls, console) || wieqi(cls, pseudo))) {
            break;
        }
        if (PostMessageW(window, WM_CLOSE, 0, 0)) {
            w->targets[i].windows++;
            w->targets[i].visible += !w->hidden;
        }
        break;
    }
    return 1;
}

// closeEntry asks the running copies of path to close and waits for them.
// It returns 0 after a cancel.
static int closeEntry(MSIHANDLE install, const WCHAR *path, DWORD timeout, closeTarget *targets, WCHAR *image)
{
    DWORD n = findRunning(install, path, 0, targets, image);
    if (!n) {
        logft(install, tmplClose, path, msgNoneRunning, 0);
        return 1;
    }
    closeWindows w = {targets, n, 0};
    EnumWindows(postClose, (INTPTR)&w);
    w.hidden = 1;
    EnumWindows(postClose, (INTPTR)&w);
    unsigned long long deadline = GetTickCount64() + timeout;
    int ok = 1;
    for (DWORD i = 0; i < n && ok; i++) {
        if (!targets[i].windows) {
            logPid(install, msgNoWindow, targets[i].pid, path, 0);
            continue;
        }
        logPid(install, msgAsked, targets[i].pid, path, 0);
        DWORD wait = WAIT_TIMEOUT;
        for (;;) {
            unsigned long long now = GetTickCount64();
            DWORD slice = now >= deadline ? 0 : deadline - now > CANCEL_POLL_MS ? CANCEL_POLL_MS : (DWORD)(deadline - now);
            wait = WaitForSingleObject(targets[i].process, slice);
            if (wait != WAIT_TIMEOUT || slice == 0) {
                break;
            }
            if (cancelled(install)) {
                ok = 0;
                break;
            }
        }
        if (ok) {
            logPid(install, wait == WAIT_OBJECT_0 ? msgExited : msgStillRunning, targets[i].pid, path, 0);
        }
    }
    for (DWORD i = 0; i < n; i++) {
        CloseHandle(targets[i].process);
    }
    return ok;
}

// closeLine handles one line of BuilderCloseApps. It returns -1 for
// malformed data, 0 after a cancel and 1 otherwise.
static int closeLine(MSIHANDLE install, WCHAR *line, closeTarget *targets, WCHAR *image)
{
    WCHAR *spec = field(line, 0x1F);
    WCHAR *tmpl = spec ? field(spec, 0x1F) : 0;
    DWORD timeout = 0;
    if (!tmpl || !hexfield(spec, &timeout)) {
        logft(install, tmplClose, msgCloseData, 0, 0);
        return -1;
    }
    if (*line) {
        int cond = MsiEvaluateConditionW(install, line);
        if (cond == 0) {
            return 1;
        }
        if (cond != MSICONDITION_TRUE && cond != MSICONDITION_NONE) {
            logft(install, tmplClose, msgCondition, line, 0);
            return 1;
        }
    }
    WCHAR *path = format(install, tmpl);
    int ok = 1;
    if (!path || !*path || hasControl(path, 1)) {
        logft(install, tmplClose, msgFormat, tmpl, 0);
    } else {
        ok = closeEntry(install, path, timeout, targets, image);
    }
    HeapFree(GetProcessHeap(), 0, path);
    return ok;
}

__declspec(dllexport) UINT WINAPI BuilderCloseSchedule(MSIHANDLE install)
{
    HANDLE heap = GetProcessHeap();
    WCHAR *list = property(install, propCloseApps);
    WCHAR *image = HeapAlloc(heap, 0, IMAGE_PATH * sizeof(WCHAR));
    closeTarget *targets = HeapAlloc(heap, 0, CLOSE_MAX * sizeof(closeTarget));
    UINT result = ERROR_SUCCESS;
    if (!image || !targets) {
        logft(install, tmplClose, msgNoMemory, 0, 0);
    }
    for (WCHAR *line = list; image && targets && line && *line;) {
        WCHAR *next = field(line, '\n');
        int ok = closeLine(install, line, targets, image);
        if (ok < 0) {
            break;
        }
        if (ok == 0) {
            result = ERROR_INSTALL_USEREXIT;
            break;
        }
        line = next;
    }
    HeapFree(heap, 0, targets);
    HeapFree(heap, 0, image);
    HeapFree(heap, 0, list);
    return result;
}

// endRunning ends the processes of one path and returns how many it found;
// *ended counts those it ended or found gone.
static DWORD endRunning(MSIHANDLE install, const WCHAR *path, closeTarget *targets, WCHAR *image, DWORD *ended)
{
    DWORD n = findRunning(install, path, PROCESS_TERMINATE, targets, image);
    *ended = 0;
    for (DWORD i = 0; i < n; i++) {
        HANDLE process = targets[i].process;
        if (WaitForSingleObject(process, 0) == WAIT_OBJECT_0) {
            logPid(install, msgGone, targets[i].pid, path, 0);
            (*ended)++;
        } else if (!TerminateProcess(process, ERROR_INSTALL_USEREXIT)) {
            logPid(install, msgEndFailed, targets[i].pid, path, GetLastError());
        } else {
            DWORD wait = WaitForSingleObject(process, TERMINATE_WAIT_MS);
            logPid(install, wait == WAIT_OBJECT_0 ? msgEnded : msgNotGone, targets[i].pid, path, 0);
            *ended += wait == WAIT_OBJECT_0;
        }
        CloseHandle(process);
    }
    return n;
}

__declspec(dllexport) UINT WINAPI BuilderClose(MSIHANDLE install)
{
    HANDLE heap = GetProcessHeap();
    WCHAR *data = property(install, propData);
    WCHAR *image = HeapAlloc(heap, 0, IMAGE_PATH * sizeof(WCHAR));
    closeTarget *targets = HeapAlloc(heap, 0, CLOSE_MAX * sizeof(closeTarget));
    if (!image || !targets) {
        logft(install, tmplClose, msgNoMemory, 0, 0);
    }
    // The data starts with the builder's empty seed and ends with `|`.
    for (WCHAR *path = data; image && targets && path;) {
        WCHAR *next = field(path, '|');
        if (*path) {
            // Another pass only finds more when this one was full and
            // freed a slot by ending a process.
            DWORD ended, n = endRunning(install, path, targets, image, &ended);
            if (!n) {
                logft(install, tmplClose, path, msgNoneRunning, 0);
            }
            for (int pass = 1; n == CLOSE_MAX && ended && pass < CLOSE_PASSES; pass++) {
                n = endRunning(install, path, targets, image, &ended);
            }
        }
        path = next;
    }
    HeapFree(heap, 0, targets);
    HeapFree(heap, 0, image);
    HeapFree(heap, 0, data);
    return ERROR_SUCCESS;
}

BOOL WINAPI _DllMainCRTStartup(HANDLE instance, DWORD reason, void *reserved)
{
    (void)instance;
    (void)reason;
    (void)reserved;
    return 1;
}
