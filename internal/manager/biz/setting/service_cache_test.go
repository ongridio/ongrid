package setting

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/setting"
)

// 暂停第一次读取的返回，让写入和失效操作在旧结果回填前完成。
type delayedSettingRepo struct {
	*fakeRepo
	delayed atomic.Bool
	read    chan struct{}
	resume  chan struct{}
}

func (r *delayedSettingRepo) Get(ctx context.Context, category, key string) (*model.Setting, error) {
	row, err := r.fakeRepo.Get(ctx, category, key)
	if r.delayed.CompareAndSwap(false, true) {
		close(r.read)
		select {
		case <-r.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return row, err
}

func TestServiceGet_DelayedReadDoesNotRestoreInvalidatedValue(t *testing.T) {
	for _, operation := range []string{"Set", "SetBatch", "Delete", "InvalidateAll"} {
		for _, populateBeforeResume := range []bool{false, true} {
			name := operation + "/empty_cache"
			if populateBeforeResume {
				name = operation + "/fresh_read_before_old_read"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				repo := &delayedSettingRepo{
					fakeRepo: newFakeRepo(),
					read:     make(chan struct{}),
					resume:   make(chan struct{}),
				}
				const category, key = "llm", "openai_model"
				if _, err := repo.Set(ctx, category, key, "old-model", false); err != nil {
					t.Fatal(err)
				}
				svc := New(repo, nil)
				done := make(chan struct{})
				var resumeOnce sync.Once
				resume := func() { resumeOnce.Do(func() { close(repo.resume) }) }
				t.Cleanup(func() {
					resume()
					cancel()
					<-done
				})
				go func() {
					defer close(done)
					defer func() {
						if p := recover(); p != nil {
							t.Errorf("delayed Get panicked: %v", p)
						}
					}()
					// 与写入重叠的调用可以返回旧快照，但不能污染后续读取。
					if _, _, err := svc.Get(ctx, category, key); err != nil && ctx.Err() == nil {
						t.Errorf("delayed Get: %v", err)
					}
				}()
				select {
				case <-repo.read:
				case <-ctx.Done():
					t.Fatal("timed out waiting for the old repository read")
				}

				want, wantFound := mutateSettingDuringRead(ctx, t, svc, repo, operation, category, key)
				assertCurrentValue := func() {
					t.Helper()
					value, found, err := svc.Get(ctx, category, key)
					if err != nil || value != want || found != wantFound {
						t.Fatalf("Get = (%q, %v, %v); want (%q, %v, nil)", value, found, err, want, wantFound)
					}
				}
				if populateBeforeResume {
					assertCurrentValue()
				}
				resume()
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("timed out waiting for the delayed Get to finish")
				}
				assertCurrentValue()
				assertCurrentValue()
			})
		}
	}
}

func mutateSettingDuringRead(ctx context.Context, t *testing.T, svc *Service, repo Repo, operation, category, key string) (string, bool) {
	t.Helper()
	var err error
	want, wantFound := "new-model", true
	switch operation {
	case "Set":
		err = svc.Set(ctx, category, key, want, false)
	case "SetBatch":
		err = svc.SetBatch(ctx, []model.Setting{{Category: category, Key: key, Value: want}})
	case "Delete":
		err = svc.Delete(ctx, category, key)
		want, wantFound = "", false
	case "InvalidateAll":
		// 模拟直接修改数据库后，调用公开失效入口刷新配置。
		_, err = repo.Set(ctx, category, key, want, false)
		svc.InvalidateAll()
	default:
		t.Fatalf("unknown operation: %s", operation)
	}
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	return want, wantFound
}
