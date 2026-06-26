interface ConfirmDialogProps {
  open: boolean
  title: string
  description: string
  confirmLabel: string
  loading?: boolean
  onConfirm: () => void
  onCancel: () => void
}

export function ConfirmDialog({ open, title, description, confirmLabel, loading = false, onConfirm, onCancel }: ConfirmDialogProps) {
  if (!open) {
    return null
  }

  return (
    <div className="modal-backdrop" role="presentation" onClick={loading ? undefined : onCancel}>
      <div className="modal-card" role="dialog" aria-modal="true" aria-labelledby="confirm-dialog-title" onClick={(event) => event.stopPropagation()}>
        <div className="modal-copy">
          <div className="eyebrow">Confirm</div>
          <h3 id="confirm-dialog-title">{title}</h3>
          <p className="muted">{description}</p>
        </div>
        <div className="modal-actions">
          <button type="button" className="toolbar-button subtle" onClick={onCancel} disabled={loading}>
            取消
          </button>
          <button type="button" className="danger-button" onClick={onConfirm} disabled={loading}>
            {loading ? '处理中...' : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}
