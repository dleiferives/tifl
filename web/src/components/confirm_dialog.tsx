import { createEffect, createSignal, Show } from "solid-js";

export interface ConfirmDialogRequest {
  title: string;
  message: string;
  confirmLabel: string;
  cancelLabel?: string;
  onConfirm: () => void | Promise<void>;
}

export function ConfirmDialog(props: { request: ConfirmDialogRequest | null; onCancel: () => void }) {
  const [submitting, setSubmitting] = createSignal(false);

  createEffect(() => {
    props.request;
    setSubmitting(false);
  });

  const confirm = async () => {
    const request = props.request;
    if (!request || submitting()) {
      return;
    }
    setSubmitting(true);
    try {
      await request.onConfirm();
      props.onCancel();
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Show when={props.request}>
      {(request) => (
        <div class="dialog-backdrop" role="presentation">
          <section class="confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="confirm-dialog-title">
            <h2 id="confirm-dialog-title">{request().title}</h2>
            <p>{request().message}</p>
            <div class="confirm-actions">
              <button class="secondary-button" type="button" disabled={submitting()} onClick={props.onCancel}>
                {request().cancelLabel || "Cancel"}
              </button>
              <button class="danger-button solid-danger" type="button" disabled={submitting()} onClick={() => void confirm()}>
                {submitting() ? "Deleting..." : request().confirmLabel}
              </button>
            </div>
          </section>
        </div>
      )}
    </Show>
  );
}
