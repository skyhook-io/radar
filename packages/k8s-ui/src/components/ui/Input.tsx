import { forwardRef } from 'react'

/** The shared text-input primitive. A minimal pass-through over a native
 *  `<input>` that bakes in autocorrect/autocapitalize OFF — in a Kubernetes
 *  tool every text field is an identifier, selector, or number, never prose,
 *  so browser text-mangling (e.g. macOS Safari turning `aws-node` into
 *  `Was-node`) only ever corrupts input. Spellcheck is deliberately left
 *  untouched — its red squiggle is a passive hint, not a mutation, and stays
 *  useful. Carries no styling of its own: callers keep their own `className`.
 *  Non-text inputs (checkbox, number, password) stay raw `<input>` — this
 *  defaults to `type="text"`. */
export const Input = forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(
  function Input({ type = 'text', ...props }, ref) {
    return (
      <input
        ref={ref}
        type={type}
        autoCorrect="off"
        autoCapitalize="off"
        {...props}
      />
    )
  },
)
