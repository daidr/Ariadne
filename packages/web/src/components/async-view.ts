import { defineAsyncComponent, type AsyncComponentLoader, type Component } from 'vue'
import AsyncViewError from './AsyncViewError.vue'
import AsyncViewLoading from './AsyncViewLoading.vue'

/**
 * Wrap a lazy view chunk with a delayed spinner (avoiding flashes on fast networks)
 * and a reload fallback if chunk loading fails after a network blip or server upgrade.
 */
export function defineAsyncView<T extends Component = Component>(loader: AsyncComponentLoader<T>) {
  return defineAsyncComponent({
    loader,
    loadingComponent: AsyncViewLoading,
    errorComponent: AsyncViewError,
    delay: 150,
    onError(_error, retry, fail, attempts) {
      if (attempts <= 1) retry()
      else fail()
    },
  })
}
