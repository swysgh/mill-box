package io.nekohasekai.sfa.vendor

import androidx.camera.core.ImageAnalysis
import io.nekohasekai.sfa.compose.screen.qrscan.QRCodeCropArea

object Vendor : VendorInterface {
    override fun createQRCodeAnalyzer(
        onSuccess: (String) -> Unit,
        onFailure: (Exception) -> Unit,
        onCropArea: ((QRCodeCropArea?) -> Unit)?,
    ): ImageAnalysis.Analyzer? = null
}
