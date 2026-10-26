import { useMediaQuery } from './useMediaQuery'

// A phone: a narrow screen whose primary pointer is touch. A desktop browser
// narrowed below this width keeps the desktop layout (and its content floor),
// since a wider window can show the full UI; browser dev-tools device mode
// emulates touch and gets the phone layout. Keep in sync with the `phone:`
// variant in theme/tailwind-theme.css.
export const PHONE_MEDIA_QUERY = '(max-width: 639px) and (pointer: coarse)'

export function usePhoneLayout(): boolean {
  return useMediaQuery(PHONE_MEDIA_QUERY)
}
