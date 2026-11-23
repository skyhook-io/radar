import { RIGHTSIZING_SCAN_PATH } from '../../utils/page-links'
import type { ComponentPropsWithoutRef } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { useNavCustomization } from '../../context/NavCustomization'

export function RightsizingScanLink(props: Omit<ComponentPropsWithoutRef<'a'>, 'href' | 'onClick' | 'target'>) {
  const { search } = useLocation()
  const { fleetTakeoverHref, onHostNavigate } = useNavCustomization()
  const href = fleetTakeoverHref?.('rightsizing', search)
  if (!href) return <Link {...props} to={{ pathname: RIGHTSIZING_SCAN_PATH, search }} />
  return (
    <a
      {...props}
      href={href}
      onClick={(event) => {
        if (!onHostNavigate || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
        event.preventDefault()
        onHostNavigate(href)
      }}
    />
  )
}
