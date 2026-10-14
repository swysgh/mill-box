package io.nekohasekai.sfa.vendor

import androidx.camera.core.ImageAnalysis
import io.nekohasekai.sfa.compose.screen.qrscan.QRCodeCropArea

interface VendorInterface {
    fun createQRCodeAnalyzer(
        onSuccess: (String) -> Unit,
        onFailure: (Exception) -> Unit,
        onCropArea: ((QRCodeCropArea?) -> Unit)? = null,
    ): ImageAnalysis.Analyzer?

    fun isPerAppProxyAvailable(): Boolean = true
}
