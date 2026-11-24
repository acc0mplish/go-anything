# WORK — 작업 결과물 보관 폴더

도구 실행·에이전트 세션·검증으로 **생긴 결과물**은 모두 이 폴더 아래 둔다.
소스 코드·문서와 결과물이 섞이지 않도록 두 폴더로 구분한다. `WORK/` 안에서 이 파일만 Git에 추적된다.

| 폴더 | 용도 | 무엇을 넣는가 |
|---|---|---|
| `Backup/` | **기존 데이터** 보관 | 2026-11-23 이전 세션의 산출물. 참조용으로만 쓰고 고치지 않는다 |
| `Result/` | **새 결과물** | 2026-11-23 이후 작업 결과. 작업 ID별 폴더를 만든다 |

## 규칙

1. 결과물은 `WORK/Result/<작업-ID>/` 아래에 쓴다. 문서 폴더나 소스 폴더에는 쓰지 않는다.
   작업 ID는 `docs/task-id/<작업-ID>/state.json`의 ID와 같게 맞춘다.
2. `Backup/`의 파일은 수정하지 않는다. 다시 쓸 일이 있으면 `Result/`로 복사해서 쓴다.
3. 도구가 자동으로 남기는 산출물(라운드별 실행 기록 `.go-anything/checks/`, 분석 리포트 `.go-anything/reports/`,
   보안 확인서 `.go-anything/security/`)은 대상 모듈 폴더를 `WORK/Result/<작업-ID>/` 안에 두면 자연스럽게 이 폴더에 남는다.
4. 빌드 산출물(`bin/`)과 작업 상태(`docs/task-id/`)는 결과물이 아니므로 각자의 `.gitignore` 규칙을 따른다.
5. `WORK/` 안의 파일은 Git에 커밋하지 않는다(`WORK/*`를 무시하고 `WORK/README.md`만 추적).

## 이 저장소의 Backup에 든 것 (2026-11-23 이동, 삭제 0건)

| 원래 위치 | 현재 위치 | 내용 |
|---|---|---|
| `docs/task-id/klic-cleanroom/` | `WORK/Backup/docs/task-id/klic-cleanroom/` | 취소된 작업(ID `klic-cleanroom`)의 계획 문서·실행 도구·하위 에이전트 호출 프롬프트. 상태는 `cancelled` |
| `.go-anything/` | `WORK/Backup/.go-anything/` | 이 저장소를 스스로 대상으로 돌린 분석 리포트·보안 확인서 (추적본은 `docs/reports/`에 있음) |
| 루트의 `go-anything` 바이너리 | `WORK/Backup/bin/go-anything-v0.40.0-linux-amd64` | 2026-11-22에 빌드한 v0.40.0 실행 파일 |
| `eGov-anything/` (저장소 안) | `WORK/Backup/claude-mem-stubs/` | claude-mem 플러그인이 다른 프로젝트 경로를 잘못 이어 만든 169바이트 `CLAUDE.md` 스텁 |

이동 전후 대조 기록: `WORK/Result/layout-work/` (파일 크기·SHA-256 목록).
