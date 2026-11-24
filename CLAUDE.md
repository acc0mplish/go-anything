# go-anything

**세션 시작 시 `docs/STATUS.md`를 먼저 읽는다.** 지금 하는 일과 다음 할 일은 그 문서에만 적는다.
전체 흐름은 `docs/WORKFLOW.md`, 용어는 `docs/TERMS.md`, 결과물은 `WORK/`에 있다.

## 프로젝트 정체

원본 프로그램의 동작을 기록하고, Go 재작성을 판정하고, 에이전트가 일치 완료할 때까지 고치게 하는 도구다.
eGov-anything(Java·전자정부 표준프레임워크용)과는 **별개의 독립 도구**다. 서로 의존하지 않고 공유하는 것은 기록 파일 형식뿐이다.

## 규칙

1. 증거 없이 "통과"라고 쓰지 않는다. README의 숫자는 이 저장소에서 실측한 값이다. 실패는 실패 그대로 적는다.
2. 새 모듈 의존성을 추가하지 않는다(표준 라이브러리 + pgx·go-toml·modernc sqlite). `internal/domain/compare.go`·`answer.go`는 표준 라이브러리만 쓴다(유닛 테스트에 복사된다).
3. 생성 코드·에이전트 프롬프트·주석·로그·오류 메시지는 영어다. 한국어 문서와 보고서 문구는 `docs/TERMS.md`의 표준 용어를 쓴다. `go test ./cmd/go-anything -run TestTerminology`로 확인한다.
4. 테스트 없이 도착한 PR은 머지하지 않는다. 수정에는 그 수정이 없으면 실패하는 테스트를 붙인다.
5. 검증 명령: `gofmt -l .` · `go vet ./...` · `go test -race -count=1 ./...` · `GOOS=windows go build ./...`. 종료 코드를 보고서에 적는다.
6. 결과물은 `WORK/Result/<작업-ID>/`에, 작업 상태는 `docs/task-id/<작업-ID>/state.json`에 둔다. 둘 다 Git에 커밋하지 않는다. `WORK/Backup/`은 고치지 않는다.
7. 세션을 시작할 때와 끝낼 때 `docs/STATUS.md`의 "지금 하는 일"을 고친다. 긴 로그는 STATUS에 쓰지 않는다.
8. 버전·태그·릴리스는 사용자가 정한다. 에이전트가 임의로 올리지 않는다.

## 환경 주의

- 빌드 출력은 저장소 밖이나 `bin/`에 둔다: `go build -o /tmp/go-anything ./cmd/go-anything`. 출력 이름을 생략한 `go build ./cmd/go-anything`은 쓰지 않는다.
- claude-mem 플러그인 훅이 `<작업 디렉터리>/eGov-anything/**/CLAUDE.md` 같은 빈 스텁을 계속 만든다. 이 저장소는 `CLAUDE.md` 스텁을 무시하도록 `.gitignore`에 규칙을 두었다. 셸 `cd`는 서브셸 `( cd … && … )` 안에서만 쓴다.
