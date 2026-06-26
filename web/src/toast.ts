import Swal from 'sweetalert2'

const toast = Swal.mixin({
  toast: true,
  position: 'top-end',
  showConfirmButton: false,
  timer: 2400,
  timerProgressBar: true,
  customClass: {
    popup: 'app-toast-popup',
    title: 'app-toast-title',
  },
  didOpen: (popup) => {
    popup.addEventListener('mouseenter', Swal.stopTimer)
    popup.addEventListener('mouseleave', Swal.resumeTimer)
  },
})

export function showSuccessToast(title: string) {
  void toast.fire({ icon: 'success', title })
}

export function showErrorToast(title: string) {
  void toast.fire({ icon: 'error', title, timer: 3400 })
}
