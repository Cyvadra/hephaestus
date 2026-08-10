import { GripVertical, Plus, Trash2, X } from 'lucide-react'
import { useState, type ReactNode } from 'react'

interface FieldProps {
  label: string
  htmlFor?: string
  hint?: string
  error?: string
  wide?: boolean
  children: ReactNode
}

export function Field({ label, htmlFor, hint, error, wide, children }: FieldProps) {
  return (
    <div className={`configuration-field${wide ? ' wide' : ''}${error ? ' invalid' : ''}`}>
      <label htmlFor={htmlFor}>{label}</label>
      {children}
      {error ? <span className="configuration-field-error">{error}</span> : hint ? <span className="configuration-field-hint">{hint}</span> : null}
    </div>
  )
}

export function TextInput({ id, value, onChange, disabled, placeholder, maxLength }: { id: string; value: string; onChange: (value: string) => void; disabled?: boolean; placeholder?: string; maxLength?: number }) {
  return <input id={id} value={value} disabled={disabled} placeholder={placeholder} maxLength={maxLength} onChange={event => onChange(event.target.value)} />
}

export function TextArea({ id, value, onChange, rows = 4, placeholder }: { id: string; value: string; onChange: (value: string) => void; rows?: number; placeholder?: string }) {
  return <textarea id={id} value={value} rows={rows} placeholder={placeholder} onChange={event => onChange(event.target.value)} />
}

export function NumberInput({ id, value, onChange, nullable, min, max, step = 1 }: { id: string; value: number | null; onChange: (value: number | null) => void; nullable?: boolean; min?: number; max?: number; step?: number }) {
  return (
    <input
      id={id}
      type="number"
      value={value ?? ''}
      min={min}
      max={max}
      step={step}
      onChange={event => onChange(event.target.value === '' && nullable ? null : Number(event.target.value))}
    />
  )
}

export function Toggle({ id, checked, onChange }: { id: string; checked: boolean; onChange: (checked: boolean) => void }) {
  return (
    <label className="configuration-toggle" htmlFor={id}>
      <input id={id} type="checkbox" checked={checked} onChange={event => onChange(event.target.checked)} />
      <span aria-hidden="true" />
      <strong>{checked ? '启用' : '停用'}</strong>
    </label>
  )
}

export function TagsInput({ values, onChange, suggestions = [], placeholder = '输入后按 Enter' }: { values: string[]; onChange: (values: string[]) => void; suggestions?: string[]; placeholder?: string }) {
  const [input, setInput] = useState('')
  const add = (raw: string) => {
    const value = raw.trim()
    if (value && !values.includes(value)) onChange([...values, value])
    setInput('')
  }
  const available = suggestions.filter(value => !values.includes(value) && value.toLowerCase().includes(input.trim().toLowerCase())).slice(0, 8)
  return (
    <div className="configuration-autocomplete">
      <div className="configuration-tags-input">
        {values.map(value => (
          <span key={value} className="configuration-tag">
            {value}
            <button type="button" aria-label={`移除 ${value}`} onClick={() => onChange(values.filter(item => item !== value))}><X size={12} /></button>
          </span>
        ))}
        <input
          value={input}
          placeholder={values.length ? '继续添加…' : placeholder}
          onChange={event => setInput(event.target.value)}
          onKeyDown={event => {
            if (event.key !== 'Enter' && event.key !== ',') return
            event.preventDefault()
            add(input)
          }}
        />
      </div>
      {suggestions.length > 0 && <div className="configuration-autocomplete-menu">
        {available.length > 0 ? available.map(value => <button type="button" key={value} onMouseDown={event => event.preventDefault()} onClick={() => add(value)}><span>{value}</span><Plus size={13} /></button>) : <span>{input ? '没有匹配的系统选项，可按 Enter 自由添加' : '所有系统选项均已添加'}</span>}
      </div>}
    </div>
  )
}

export function SuggestionInput({ id, value, onChange, suggestions, placeholder }: { id: string; value: string; onChange: (value: string) => void; suggestions: string[]; placeholder?: string }) {
  const available = suggestions.filter(item => item.toLowerCase().includes(value.trim().toLowerCase())).slice(0, 8)
  return <div className="configuration-autocomplete"><input id={id} value={value} placeholder={placeholder} autoComplete="off" onChange={event => onChange(event.target.value)} />{suggestions.length > 0 && <div className="configuration-autocomplete-menu">{available.length > 0 ? available.map(item => <button type="button" key={item} onMouseDown={event => event.preventDefault()} onClick={() => onChange(item)}><span>{item}</span></button>) : <span>没有匹配的系统选项，也可以自由输入</span>}</div>}</div>
}

export function StringListEditor({ values, onChange, addLabel = '添加一项', multiline = false }: { values: string[]; onChange: (values: string[]) => void; addLabel?: string; multiline?: boolean }) {
  return (
    <div className="configuration-repeat-list">
      {values.map((value, index) => (
        <div className="configuration-repeat-row" key={index}>
          <GripVertical aria-hidden="true" size={16} />
          {multiline
            ? <textarea rows={2} value={value} onChange={event => onChange(values.map((item, itemIndex) => itemIndex === index ? event.target.value : item))} />
            : <input value={value} onChange={event => onChange(values.map((item, itemIndex) => itemIndex === index ? event.target.value : item))} />}
          <button type="button" aria-label="删除此项" onClick={() => onChange(values.filter((_, itemIndex) => itemIndex !== index))}><Trash2 size={15} /></button>
        </div>
      ))}
      <button className="configuration-add-row" type="button" onClick={() => onChange([...values, ''])}><Plus size={15} />{addLabel}</button>
    </div>
  )
}

export function Section({ title, description, children }: { title: string; description?: string; children: ReactNode }) {
  return (
    <section className="configuration-form-section">
      <header><h2>{title}</h2>{description && <p>{description}</p>}</header>
      <div className="configuration-field-grid">{children}</div>
    </section>
  )
}
