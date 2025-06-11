package resource

import "context"

const MessageTypeUpdate = "update"
const MessageTypeDelete = "delete"

type ExternalStorage[T Object[T]] interface {
	Create(ctx context.Context, item T) error
	Get(ctx context.Context, shardID string, groupKind GroupKind, objectKey ObjectKey) (T, bool, error)
	List(ctx context.Context, listOpts ListOpts) ([]T, error)
	ListPending(ctx context.Context, shardID string, groupKind GroupKind) ([]T, error)
	Update(ctx context.Context, item T) error
	UpdateStatus(ctx context.Context, item T) error
	Delete(ctx context.Context, shardID string, groupKind GroupKind, objectKey ObjectKey) error
}

type ExternalListener interface {
	Listen(f func(ctx context.Context, kind GroupKind, objectKey ObjectKey, messageType string, ack func()))
	ClearQueue(ctx context.Context) error
}
