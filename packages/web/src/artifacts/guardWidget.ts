import { useDashboardWidget, type DashboardWidgetType } from '@dou-ui/dashboard'
import { defineComponent, h, markRaw, onErrorCaptured, ref, watch, type Component } from 'vue'
import WidgetBroken from './WidgetBroken.vue'

/**
 * A widget type whose widgets show a notice in their own card when drawing them throws,
 * instead of taking the whole dashboard down with them. Changing the widget's props
 * tries it again. Drop this once `@dou-ui/dashboard` isolates widget errors itself
 * (daidr/dou-ui#338).
 */
export function guardWidget<P extends object>(
  type: DashboardWidgetType<P>,
): DashboardWidgetType<P> {
  const inner = type.component
  const component: Component = defineComponent({
    name: `Guarded${type.type}`,
    setup() {
      const { props } = useDashboardWidget()
      const broken = ref(false)
      watch(props, () => (broken.value = false))
      onErrorCaptured((error) => {
        console.error(error)
        broken.value = true
        return false
      })
      return () => (broken.value ? h(WidgetBroken) : h(inner))
    },
  })
  return { ...type, component: markRaw(component) }
}
