import { useId, useMemo, useState } from "react";
import { ArrowLeft } from "lucide-react";
import { P123QRCodeLogin } from "./P123QRCodeLogin";
import { Spider91UploadTargetField } from "./Spider91UploadTargetField";
import {
  FormState,
  Kind,
  credentialFields,
  credentialHelp,
  usesRootDirectoryID,
  rootIdPlaceholder,
} from "./constants";
import * as api from "../api";

type DriveOption = {
  kind: Kind;
  label: string;
  abbr: string;
};

const DRIVE_OPTIONS: DriveOption[] = [
  { kind: "p115", label: "115 网盘", abbr: "115" },
  { kind: "p123", label: "123 云盘", abbr: "123" },
  { kind: "pikpak", label: "PikPak", abbr: "Pk" },
  { kind: "onedrive", label: "OneDrive", abbr: "OD" },
  { kind: "googledrive", label: "Google Drive", abbr: "GD" },
  { kind: "localstorage", label: "本地存储", abbr: "Lo" },
  { kind: "spider91", label: "91 爬虫", abbr: "91" },
  { kind: "quark", label: "夸克网盘", abbr: "Qk" },
  { kind: "wopan", label: "联通沃盘", abbr: "Wo" },
];

export function DriveForm({
  form,
  onChange,
  isEdit,
  uploadTargets,
  nameError,
  onNameBlur,
  onBack,
}: {
  form: FormState;
  onChange: (f: FormState) => void;
  isEdit: boolean;
  uploadTargets: api.AdminDrive[];
  nameError?: string;
  onNameBlur?: () => void;
  onBack?: () => void;
}) {
  const idPrefix = useId();
  const fields = useMemo(() => credentialFields(form.kind), [form.kind]);
  const help = credentialHelp(form.kind, isEdit);
  const [step, setStep] = useState<"type" | "form">(isEdit ? "form" : "type");
  const nameId = `${idPrefix}-drive-name`;
  const rootId = `${idPrefix}-drive-root`;

  function set<K extends keyof FormState>(k: K, v: FormState[K]) {
    onChange({ ...form, [k]: v });
  }
  function setCred(k: string, v: string) {
    onChange({ ...form, creds: { ...form.creds, [k]: v } });
  }
  function setKind(v: Kind) {
    onChange({
      ...form,
      kind: v,
      rootId: "",
      creds: {},
    });
  }
  function selectType(kind: Kind) {
    setKind(kind);
    setStep("form");
  }
  function goBack() {
    setStep("type");
    onChange({
      ...form,
      name: "",
      rootId: "",
      creds: {},
    });
    onBack?.();
  }

  const selectedOption = DRIVE_OPTIONS.find((o) => o.kind === form.kind);

  if (step === "type" && !isEdit) {
    return (
      <div className="admin-drive-type-grid">
        {DRIVE_OPTIONS.map((opt) => (
          <button
            key={opt.kind}
            type="button"
            className="admin-drive-type-card"
            onClick={() => selectType(opt.kind)}
          >
            <span className="admin-drive-type-card__icon">
              {opt.abbr}
            </span>
            <span className="admin-drive-type-card__label">{opt.label}</span>
          </button>
        ))}
      </div>
    );
  }

  return (
    <div className="admin-form">
      {!isEdit && (
        <div className="admin-drive-step-header">
          <button type="button" className="admin-drive-step-back" onClick={goBack}>
            <ArrowLeft size={14} /> 重选类型
          </button>
          {selectedOption && (
            <span className="admin-drive-step-badge">
              <span className="admin-drive-step-badge__abbr">{selectedOption.abbr}</span>
              <span className="admin-drive-step-badge__label">{selectedOption.label}</span>
            </span>
          )}
        </div>
      )}

      <div className="admin-form__section">
        <div className="admin-form__row">
          <label htmlFor={nameId}>名称 *</label>
          <input
            id={nameId}
            value={form.name}
            onChange={(e) => set("name", e.target.value)}
            onBlur={onNameBlur}
            placeholder="给这个盘起个名字"
            className={nameError ? "is-invalid" : undefined}
            aria-invalid={nameError ? "true" : undefined}
            aria-describedby={nameError ? `${nameId}-error` : undefined}
          />
          {nameError && (
            <div className="admin-form__error" id={`${nameId}-error`}>
              {nameError}
            </div>
          )}
        </div>

        {usesRootDirectoryID(form.kind) && (
          <div className="admin-form__row">
            <label htmlFor={rootId}>根目录 ID</label>
            <input
              id={rootId}
              value={form.rootId}
              onChange={(e) => set("rootId", e.target.value)}
              placeholder={rootIdPlaceholder(form.kind)}
            />
            <div className="admin-form__help">
              留空时使用该网盘类型的默认根目录
            </div>
          </div>
        )}
      </div>

      {(help || fields.length > 0) && (
        <div className="admin-form__section">
          <h3 className="admin-form__section-label">凭证配置</h3>

          {help && (
            <div className="admin-form__help admin-form__help--lead">
              {help}
            </div>
          )}

          {form.kind === "p123" && (
            <P123QRCodeLogin
              onToken={(token) => setCred("access_token", token)}
            />
          )}

          {fields.map((f) => (
            <div key={f.key} className="admin-form__row">
              <label htmlFor={`${idPrefix}-credential-${f.key}`}>
                {f.label}
                {f.required && " *"}
              </label>
              {f.multiline ? (
                <textarea
                  id={`${idPrefix}-credential-${f.key}`}
                  value={form.creds[f.key] ?? ""}
                  onChange={(e) => setCred(f.key, e.target.value)}
                  placeholder={f.placeholder}
                />
              ) : (
                <input
                  id={`${idPrefix}-credential-${f.key}`}
                  type={credentialInputType(f.key)}
                  value={form.creds[f.key] ?? ""}
                  onChange={(e) => setCred(f.key, e.target.value)}
                  placeholder={f.placeholder}
                />
              )}
              {f.help && <div className="admin-form__help">{f.help}</div>}
            </div>
          ))}
        </div>
      )}

      {form.kind === "spider91" && (
        <div className="admin-form__section">
          <h3 className="admin-form__section-label">上传设置</h3>
          <Spider91UploadTargetField
            value={form.spider91UploadDriveId}
            onChange={(v) => set("spider91UploadDriveId", v)}
            uploadTargets={uploadTargets}
          />
        </div>
      )}
    </div>
  );
}

function credentialInputType(key: string): string {
  return /password|token|secret/i.test(key) ? "password" : "text";
}
