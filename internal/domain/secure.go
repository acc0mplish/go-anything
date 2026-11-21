package domain

import "time"

// SecureSeverity says how a checklist item weighs in the gate: a mandatory failure blocks the
// migration, a recommended failure is reported only. A manual item never blocks; it names what a
// person must verify against the official text.
type SecureSeverity string

const (
	SecureMandatory   SecureSeverity = "mandatory"
	SecureRecommended SecureSeverity = "recommended"
)

// SecureMethod says how an item is checked.
type SecureMethod string

const (
	// SecureAuto is a source pattern the tool checks itself.
	SecureAuto SecureMethod = "auto"
	// SecureTool runs an external tool when it is installed (govulncheck, go vet).
	SecureTool SecureMethod = "tool"
	// SecureManual cannot be judged from source and names what a human must verify against the
	// official document.
	SecureManual SecureMethod = "manual"
)

// SecureStatus is one item's outcome.
type SecureStatus string

const (
	SecurePass         SecureStatus = "pass"
	SecureFail         SecureStatus = "fail"
	SecureStatusManual SecureStatus = "manual"
	SecureSkip         SecureStatus = "skip"
)

// SecureItem is one row of the security function confirmation checklist.
type SecureItem struct {
	ID string `json:"id"`
	// Ref anchors the item to its clause in the official server common security requirements
	// (국가용 보안요구사항 V3.0 R1, 2023-08-21): a 소분류 number such as "7.3", or a 대분류
	// number when no 소분류 matches exactly. Product-group requirements (part 3, e.g. intrusion
	// prevention products) apply additionally when the migrated system is such a product and stay
	// out of this list.
	Ref           string         `json:"ref,omitempty"`
	Category      string         `json:"category"`
	CategoryEN    string         `json:"category_en"`
	Requirement   string         `json:"requirement"`
	RequirementEN string         `json:"requirement_en"`
	Method        SecureMethod   `json:"method"`
	Severity      SecureSeverity `json:"severity"`
	// Guidance says what a human verifies for a manual item, in Korean and English.
	Guidance   string `json:"guidance,omitempty"`
	GuidanceEN string `json:"guidance_en,omitempty"`
}

// SecureFinding is one piece of evidence for a failing item.
type SecureFinding struct {
	Item   string `json:"item"`
	Pos    string `json:"pos"`
	Detail string `json:"detail"`
}

// SecureResult is one item's outcome on one module.
type SecureResult struct {
	Item     string          `json:"item"`
	Status   SecureStatus    `json:"status"`
	Findings []SecureFinding `json:"findings,omitempty"`
	Note     string          `json:"note,omitempty"`
}

// SecureReport is the whole checklist run over one module.
type SecureReport struct {
	Root         string         `json:"root"`
	GeneratedAt  time.Time      `json:"generated_at"`
	ToolVersion  string         `json:"tool_version"`
	Items        []SecureItem   `json:"items"`
	Results      []SecureResult `json:"results"`
	GoVersion    string         `json:"go_version"`
	ModulePath   string         `json:"module_path"`
	ScannedFiles int            `json:"scanned_files"`
}

// SecureProvenance states in both languages what this checklist is and is not. The confirmation
// sheet itself is a government document this tool does not reproduce; the checklist maps the
// official server common requirements onto a Go module, and the manual items point back to the
// official clauses.
const SecureProvenance = "이 체크리스트는 국가정보원 「정보보호시스템 및 네트워크 장비 국가용 보안요구사항」(1·2편 해설 및 공통보안요구사항, 2023.8.21.)의 서버 공통보안요구사항 V3.0 R1 소분류를 Go 모듈 검사 항목으로 옮긴 운영 목록이다. 각 항목의 ref 열이 공식 조항 번호를 가리킨다. 공식 원문을 대체하지 않으며, manual 항목은 공식 문서와 대비하여 사람이 확인해야 한다. 3편 제품 보안요구사항(침입차단 제품군 등)은 이식 대상이 그런 제품일 때 추가로 적용되며 이 목록의 범위 밖이다."
const SecureProvenanceEN = "This checklist maps the server common security requirements V3.0 R1 of the Korean NIS national security requirements for information protection systems and network equipment (parts 1-2, 2023-08-21) onto checkable items for a Go module; each item's ref column points at the official clause. It does not replace the official text; manual items must be verified by a person against that text. Part 3 product-group requirements (intrusion prevention products and the like) apply additionally when the migrated system is such a product and are out of scope here."

// SecurityChecklist is the item list, grouped by the official 대분류 names, in display order.
// IDs are stable across versions: the scanner dispatches on them. Items whose behavior the tool
// judges carry Method auto or tool; everything the official text asks of the deployed product that
// source cannot show is a manual item with pointed guidance.
func SecurityChecklist() []SecureItem {
	return []SecureItem{
		{ID: "SF-1.1", Ref: "1.3", Category: "식별 및 인증", CategoryEN: "Identification and authentication",
			Requirement:   "소스에 자격증명(비밀번호, 토큰, 키)을 하드코딩하지 않는다",
			RequirementEN: "No credentials (passwords, tokens, keys) are hardcoded in source",
			Method:        SecureAuto, Severity: SecureMandatory},
		{ID: "SF-1.3", Ref: "1.1", Category: "식별 및 인증", CategoryEN: "Identification and authentication",
			Requirement:   "사용자는 제품의 기능을 쓰기 전에 식별 및 인증을 수행한다",
			RequirementEN: "Every user is identified and authenticated before any product function",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "모든 관리·서비스 엔드포인트가 인증 뒤에만 노출되는지 라우팅 표와 대비한다. go-anything rbac로 구조적으로 확인할 수 있다",
			GuidanceEN: "Check the route table: every admin and service endpoint must sit behind authentication. go-anything rbac verifies this structurally"},
		{ID: "SF-1.4", Ref: "1.2", Category: "식별 및 인증", CategoryEN: "Identification and authentication",
			Requirement:   "인증 실패가 반복되면 계정이나 접속을 잠근다",
			RequirementEN: "Repeated authentication failures lock the account or the connection",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "실패 횟수 임계값, 잠금 시간, 잠금 해제 절차를 운영 문서와 대비한다",
			GuidanceEN: "Verify the failure threshold, lockout duration and unlock procedure against the operations document"},
		{ID: "SF-1.5", Ref: "1.3", Category: "식별 및 인증", CategoryEN: "Identification and authentication",
			Requirement:   "패스워드 등 민감정보를 생성할 때 강도를 검증하고 안전하게 저장한다",
			RequirementEN: "Passwords and other secrets are strength-checked on creation and stored safely",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "비밀번호 정책(길이·복잡도), 솔트와 단방향 해시 저장, 기본값 거부를 문서와 대비한다",
			GuidanceEN: "Verify the password policy (length, complexity), salted one-way hash storage and rejection of default values"},
		{ID: "SF-1.6", Ref: "1.4", Category: "식별 및 인증", CategoryEN: "Identification and authentication",
			Requirement:   "인증 정보를 재사용(재전송)할 수 없다",
			RequirementEN: "Authentication material cannot be replayed",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "토큰·세션의 만료, nonce 일회성, 재사용 감지를 설계 문서와 대비한다",
			GuidanceEN: "Verify token and session expiry, one-time nonces and replay detection in the design document"},
		{ID: "SF-1.7", Ref: "1.5", Category: "식별 및 인증", CategoryEN: "Identification and authentication",
			Requirement:   "인증 피드백이 어떤 정보가 틀렸는지 밝히지 않는다",
			RequirementEN: "Authentication feedback does not reveal which part failed",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "로그인 실패 메시지가 아이디·비밀번호를 구분해 알려주지 않는지 확인한다",
			GuidanceEN: "Check that login failure messages do not distinguish a wrong user from a wrong password"},

		{ID: "SF-1.2", Ref: "2.4", Category: "보안관리", CategoryEN: "Security management",
			Requirement:   "기본 계정과 기본 비밀번호를 사용하지 않는다",
			RequirementEN: "No default accounts or default passwords ship with the system",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "배포 시 생성되는 계정과 초기 비밀번호 정책, 강제 변경 절차를 문서로 확인한다",
			GuidanceEN: "Verify the deployed accounts, initial password policy and forced-change procedure in the deployment docs"},
		{ID: "SF-2.2", Ref: "2.1", Category: "보안관리", CategoryEN: "Security management",
			Requirement:   "보안관리 기능이 역할로 분리되어 인가된 관리자만 쓸 수 있다",
			RequirementEN: "Security management functions are separated by role and reserved to authorized administrators",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "관리자 역할 정의와 위임 구조를 문서와 대비한다. go-anything rbac가 허용·거부 결과를 정책과 대비해 확인한다",
			GuidanceEN: "Verify the administrator roles and delegation against the documentation. go-anything rbac audits allow and deny outcomes against a declared policy"},
		{ID: "SF-2.3", Ref: "2.2", Category: "보안관리", CategoryEN: "Security management",
			Requirement:   "관리 접속이 암호화된 채널로만 이루어진다",
			RequirementEN: "Administrative access happens only over an encrypted channel",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "관리 콘솔·API의 TLS 강제와 평문 포통 폐쇄를 확인한다",
			GuidanceEN: "Verify that the admin console and API enforce TLS and plaintext ports are closed"},
		{ID: "SF-2.4", Ref: "2.3", Category: "보안관리", CategoryEN: "Security management",
			Requirement:   "보안관리 접속을 허용된 IP로 제한한다",
			RequirementEN: "Administrative access is restricted to allowed IP addresses",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "관리 엔드포인트의 IP 허용 목록(방화벽, 프록시 규칙 포함)을 운영 문서와 대비한다",
			GuidanceEN: "Verify the IP allowlist on admin endpoints, including firewall and proxy rules, against the operations document"},
		{ID: "SF-2.5", Ref: "2.5", Category: "보안관리", CategoryEN: "Security management",
			Requirement:   "에이전트를 관리(등록·제거·상태 확인)할 수 있다",
			RequirementEN: "Agents can be managed: enrolled, removed and health-checked",
			Method:        SecureManual, Severity: SecureRecommended,
			Guidance:   "에이전트형 제품에만 적용한다. 에이전트가 없으면 이 항목은 해당 없음으로 기록한다",
			GuidanceEN: "Applies to agent-based products only. With no agent, record this item as not applicable"},

		{ID: "SF-3.4", Ref: "3.1", Category: "데이터 보호", CategoryEN: "Data protection",
			Requirement:   "TLS 인증서 검증을 우회(InsecureSkipVerify)하지 않는다",
			RequirementEN: "TLS certificate verification is never bypassed (InsecureSkipVerify)",
			Method:        SecureAuto, Severity: SecureMandatory},
		{ID: "SF-3.5", Ref: "3.1", Category: "데이터 보호", CategoryEN: "Data protection",
			Requirement:   "민감정보를 평문 프로토콜(http://)로 전송하지 않는다",
			RequirementEN: "Sensitive data is not sent over plaintext protocol (http://) endpoints",
			Method:        SecureAuto, Severity: SecureRecommended},
		{ID: "SF-3.9", Ref: "3.2", Category: "데이터 보호", CategoryEN: "Data protection",
			Requirement:   "저장되는 민감정보를 암호화하거나 안전하게 보호한다",
			RequirementEN: "Stored sensitive data is encrypted or otherwise protected at rest",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "파일·데이터베이스에 저장되는 민감정보의 암호화와 키 보관을 설계 문서와 대비한다",
			GuidanceEN: "Verify encryption and key custody for sensitive data stored in files and databases against the design document"},

		{ID: "SF-4.4", Ref: "4.1", Category: "자체보호", CategoryEN: "Self-protection",
			Requirement:   "시작이나 초기화 때 보안기능 자체 시험을 수행한다",
			RequirementEN: "The product self-tests its security functions at startup or initialization",
			Method:        SecureManual, Severity: SecureRecommended,
			Guidance:   "시작 시 설정·무결성 점검과 실패 시 동작(안전한 거부)을 확인한다",
			GuidanceEN: "Verify startup configuration and integrity checks, and that failure fails closed"},
		{ID: "SF-4.5", Ref: "4.2", Category: "자체보호", CategoryEN: "Self-protection",
			Requirement:   "자체 구성요소와 업데이트의 무결성을 검증한다",
			RequirementEN: "The integrity of own components and updates is verified",
			Method:        SecureManual, Severity: SecureRecommended,
			Guidance:   "바이너리·설정 파일의 무결성 검사 방식과 위변조 시 동작을 확인한다",
			GuidanceEN: "Verify how binary and configuration integrity is checked and what happens on tampering"},

		{ID: "SF-5.3", Ref: "5.1", Category: "업데이트 보호", CategoryEN: "Update protection",
			Requirement:   "업데이트와 배포물이 서명되고 검증된다",
			RequirementEN: "Updates and distributed artifacts are signed and verified",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "배포 서명·체크섬 절차를 확인한다. go-anything package가 만드는 배포물에 서명과 체크섬을 포함시킨다",
			GuidanceEN: "Verify the release signing and checksum procedure. Include signatures and checksums in the artifacts go-anything package produces"},

		{ID: "SF-6.1", Ref: "6", Category: "안전한 세션 관리", CategoryEN: "Safe session management",
			Requirement:   "쿠키는 Secure와 HttpOnly 속성과 함께 발급한다",
			RequirementEN: "Cookies are issued with Secure and HttpOnly attributes",
			Method:        SecureAuto, Severity: SecureMandatory},
		{ID: "SF-6.2", Ref: "6.1", Category: "안전한 세션 관리", CategoryEN: "Safe session management",
			Requirement:   "세션을 잠그고 종료할 수 있다",
			RequirementEN: "Sessions can be locked and terminated",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "비활동 시간 잠금, 사용자·관리자 종료, 재인증 요건을 확인한다",
			GuidanceEN: "Verify idle lockout, user and administrator termination, and reauthentication requirements"},
		{ID: "SF-6.3", Ref: "6.2", Category: "안전한 세션 관리", CategoryEN: "Safe session management",
			Requirement:   "동시접속 세션 수를 제한한다",
			RequirementEN: "Concurrent sessions per user are limited",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "계정당 동시 세션 상한과 초과 시 정책(차단·기존 종료)을 확인한다",
			GuidanceEN: "Verify the per-account concurrent session cap and the policy on exceeding it (reject or evict)"},

		{ID: "SF-5.1", Ref: "7.1", Category: "감사기록", CategoryEN: "Audit logging",
			Requirement:   "인증·권한 이벤트가 로그로 남는다",
			RequirementEN: "Authentication and authorization events are logged",
			Method:        SecureAuto, Severity: SecureRecommended},
		{ID: "SF-5.2", Ref: "7.1", Category: "감사기록", CategoryEN: "Audit logging",
			Requirement:   "로그에 비밀번호·토큰·개인정보를 기록하지 않는다",
			RequirementEN: "Passwords, tokens and personal data are never written to logs",
			Method:        SecureAuto, Severity: SecureMandatory},
		{ID: "SF-5.4", Ref: "7.2", Category: "감사기록", CategoryEN: "Audit logging",
			Requirement:   "감사기록을 조회할 수 있다",
			RequirementEN: "Audit records can be queried",
			Method:        SecureManual, Severity: SecureRecommended,
			Guidance:   "기록 조회 도구와 검색 조건(시간·사용자·이벤트)을 확인한다",
			GuidanceEN: "Verify the query facility and its filters (time, user, event)"},
		{ID: "SF-5.5", Ref: "7.3", Category: "감사기록", CategoryEN: "Audit logging",
			Requirement:   "감사기록이 변조되지 않도록 보호한다",
			RequirementEN: "Audit records are protected against tampering",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "기록의 append-only 보관, 권한 분리, 원격 전송 중 하나 이상을 확인한다",
			GuidanceEN: "Verify at least one of append-only storage, privilege separation or remote forwarding"},
		{ID: "SF-5.6", Ref: "7.4", Category: "감사기록", CategoryEN: "Audit logging",
			Requirement:   "감사기록 손실이 예측되면 대응 행동을 취한다",
			RequirementEN: "A predicted loss of audit records triggers a response",
			Method:        SecureManual, Severity: SecureRecommended,
			Guidance:   "저장 공간 임계값 알림과 임계 초과 시 동작(알림·기록 유지)을 확인한다",
			GuidanceEN: "Verify storage threshold alerts and the behavior on exceeding them"},
		{ID: "SF-5.7", Ref: "7.5", Category: "감사기록", CategoryEN: "Audit logging",
			Requirement:   "감사기록이 손실되지 않도록 한다",
			RequirementEN: "Audit records are not lost",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "휘발성 메모리 버퍼의 크기와 디스크 기록 시점을 확인한다",
			GuidanceEN: "Verify the size of any volatile in-memory buffer and when records reach durable storage"},

		{ID: "SF-3.1", Ref: "8.1", Category: "암호지원", CategoryEN: "Cryptographic support",
			Requirement:   "약한 해시(MD5, SHA-1)를 보안 목적으로 사용하지 않는다",
			RequirementEN: "Weak hashes (MD5, SHA-1) are not used for security purposes",
			Method:        SecureAuto, Severity: SecureMandatory},
		{ID: "SF-3.2", Ref: "8.1", Category: "암호지원", CategoryEN: "Cryptographic support",
			Requirement:   "약한 암호 알고리즘(DES, 3DES, RC4)을 사용하지 않는다",
			RequirementEN: "Weak ciphers (DES, 3DES, RC4) are not used",
			Method:        SecureAuto, Severity: SecureMandatory},
		{ID: "SF-3.3", Ref: "8.2", Category: "암호지원", CategoryEN: "Cryptographic support",
			Requirement:   "보안 값 생성에 crypto/rand를 쓰고 math/rand를 쓰지 않는다",
			RequirementEN: "Security values come from crypto/rand, never math/rand",
			Method:        SecureAuto, Severity: SecureMandatory},
		{ID: "SF-3.6", Ref: "8.2", Category: "암호지원", CategoryEN: "Cryptographic support",
			Requirement:   "암호키를 정해진 길이와 방식으로 생성한다",
			RequirementEN: "Cryptographic keys are generated at the required length and manner",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "키 길이(예: 대칭 128비트 이상)와 생성 엔트로피 원천을 확인한다",
			GuidanceEN: "Verify key lengths (e.g. at least 128-bit symmetric) and the entropy source"},
		{ID: "SF-3.7", Ref: "8.3", Category: "암호지원", CategoryEN: "Cryptographic support",
			Requirement:   "암호키를 안전하게 저장한다",
			RequirementEN: "Cryptographic keys are stored safely",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "키 파일 권한, 키 보관소 사용, 코드·설정 내 키 금지를 확인한다",
			GuidanceEN: "Verify key file permissions, use of a key store, and that no key sits in code or configuration"},
		{ID: "SF-3.8", Ref: "8.4", Category: "암호지원", CategoryEN: "Cryptographic support",
			Requirement:   "암호키를 안전하게 파기한다",
			RequirementEN: "Cryptographic keys are destroyed safely",
			Method:        SecureManual, Severity: SecureMandatory,
			Guidance:   "교체·폐기 시 키와 파생 데이터의 삭제 절차를 확인한다",
			GuidanceEN: "Verify the deletion procedure for keys and derived data on rotation and retirement"},

		{ID: "SF-4.1", Ref: "9.1", Category: "취약성 대응", CategoryEN: "Vulnerability handling",
			Requirement:   "SQL을 문자열 조립(덧셈, Sprintf)으로 만들지 않는다",
			RequirementEN: "SQL is never assembled by string concatenation or Sprintf",
			Method:        SecureAuto, Severity: SecureMandatory},
		{ID: "SF-4.2", Ref: "9.1", Category: "취약성 대응", CategoryEN: "Vulnerability handling",
			Requirement:   "셸 명령을 문자열 조립으로 실행하지 않는다",
			RequirementEN: "Shell commands are not executed from assembled strings",
			Method:        SecureAuto, Severity: SecureMandatory},
		{ID: "SF-4.3", Ref: "9.1", Category: "취약성 대응", CategoryEN: "Vulnerability handling",
			Requirement:   "외부 입력으로 만든 파일 경로가 허용 범위 안으로 검증된다",
			RequirementEN: "File paths built from external input are validated against an allowlist",
			Method:        SecureManual, Severity: SecureRecommended,
			Guidance:   "파일 업로드·다운로드 경로의 정규화와 접두어 검증을 코드 리뷰로 확인한다",
			GuidanceEN: "Review path canonicalization and prefix checks on upload and download paths"},
		{ID: "SF-2.1", Ref: "9.3", Category: "취약성 대응", CategoryEN: "Vulnerability handling",
			Requirement:   "디버그·관리 엔드포인트(pprof, expvar)가 운영 빌드에 노출되지 않는다",
			RequirementEN: "Debug and admin endpoints (pprof, expvar) are not exposed in production builds",
			Method:        SecureAuto, Severity: SecureMandatory},
		{ID: "SF-7.1", Ref: "9.2", Category: "취약성 대응", CategoryEN: "Vulnerability handling",
			Requirement:   "알려진 취약점이 없는지 govulncheck로 확인한다",
			RequirementEN: "govulncheck reports no known vulnerabilities",
			Method:        SecureTool, Severity: SecureMandatory},
		{ID: "SF-7.2", Ref: "9.2", Category: "취약성 대응", CategoryEN: "Vulnerability handling",
			Requirement:   "의존성이 go.sum으로 고정되어 있다",
			RequirementEN: "Dependencies are pinned by go.sum",
			Method:        SecureAuto, Severity: SecureMandatory},
		{ID: "SF-10.1", Ref: "9.1", Category: "취약성 대응", CategoryEN: "Vulnerability handling",
			Requirement:   "go vet이 경고 없이 통과한다",
			RequirementEN: "go vet passes without findings",
			Method:        SecureTool, Severity: SecureMandatory},
	}
}

// SecureItemByID returns the checklist item with the given ID.
func SecureItemByID(items []SecureItem, id string) (SecureItem, bool) {
	for _, it := range items {
		if it.ID == id {
			return it, true
		}
	}
	return SecureItem{}, false
}

// MandatoryFailures lists the results that fail a mandatory item.
func (r SecureReport) MandatoryFailures() []SecureResult {
	var out []SecureResult
	for _, res := range r.Results {
		it, ok := SecureItemByID(r.Items, res.Item)
		if ok && res.Status == SecureFail && it.Severity == SecureMandatory {
			out = append(out, res)
		}
	}
	return out
}

// CountsPerStatus tallies results by status.
func (r SecureReport) CountsPerStatus() map[SecureStatus]int {
	out := map[SecureStatus]int{}
	for _, res := range r.Results {
		out[res.Status]++
	}
	return out
}
